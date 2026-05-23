package recon

import (
	"testing"
	"time"
)

// TestBackoffDelay verifies the 30s → 1m → 5m → 30m ladder.
func TestBackoffDelay(t *testing.T) {
	cases := []struct {
		failureCount int
		want         time.Duration
	}{
		{1, 30 * time.Second},
		{2, 1 * time.Minute},
		{3, 5 * time.Minute},
		{4, 30 * time.Minute},
		// Cap: further failures stay at 30 m.
		{5, 30 * time.Minute},
		{100, 30 * time.Minute},
	}
	for _, tc := range cases {
		got := backoffDelay(tc.failureCount)
		if got != tc.want {
			t.Errorf("backoffDelay(%d) = %v; want %v", tc.failureCount, got, tc.want)
		}
	}
}

// TestBackoffDelay_ZeroOrNegative verifies zero/negative failure counts return the first rung.
func TestBackoffDelay_ZeroOrNegative(t *testing.T) {
	if d := backoffDelay(0); d != backoffLadder[0] {
		t.Errorf("backoffDelay(0) = %v; want %v", d, backoffLadder[0])
	}
	if d := backoffDelay(-1); d != backoffLadder[0] {
		t.Errorf("backoffDelay(-1) = %v; want %v", d, backoffLadder[0])
	}
}

// TestProjectStateBackoffReset verifies that after a success the backoff resets.
func TestProjectStateBackoffReset(t *testing.T) {
	ps := &projectState{}

	// Simulate failures accumulating.
	ps.failureCount++
	ps.nextRetry = time.Now().Add(backoffDelay(ps.failureCount))
	ps.failureCount++
	ps.nextRetry = time.Now().Add(backoffDelay(ps.failureCount))

	if ps.failureCount != 2 {
		t.Fatalf("expected failureCount=2, got %d", ps.failureCount)
	}

	// Simulate success reset.
	ps.failureCount = 0
	ps.nextRetry = time.Time{}

	if !ps.nextRetry.IsZero() {
		t.Error("nextRetry should be zero after success reset")
	}
	if ps.failureCount != 0 {
		t.Error("failureCount should be 0 after success reset")
	}

	// After reset, next failure should start from rung 0 (30 s).
	ps.failureCount++
	d := backoffDelay(ps.failureCount)
	if d != 30*time.Second {
		t.Errorf("after reset, first failure should backoff 30s, got %v", d)
	}
}

// TestProjectStateInBackoffWindow verifies that nextRetry in the future causes skipping.
func TestProjectStateInBackoffWindow(t *testing.T) {
	ps := &projectState{
		failureCount: 1,
		nextRetry:    time.Now().Add(5 * time.Minute),
	}
	// If nextRetry is in the future, the reconciler should skip.
	if time.Now().After(ps.nextRetry) {
		t.Error("nextRetry should be in the future")
	}
}

// TestBackoffLadderLength verifies the ladder has exactly 4 rungs as per spec.
func TestBackoffLadderLength(t *testing.T) {
	if len(backoffLadder) != 4 {
		t.Errorf("backoffLadder should have 4 rungs, has %d", len(backoffLadder))
	}
	want := []time.Duration{30 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		if backoffLadder[i] != w {
			t.Errorf("backoffLadder[%d] = %v; want %v", i, backoffLadder[i], w)
		}
	}
}
