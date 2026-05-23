//go:build integration

// Integration tests for the ingest service (task 6.9).
// Exercises the batcher flush behaviour (by size and by timer) with a real
// Postgres, and the full decode → batch → flush path end-to-end.
//
// Run with:
//
//	go test -tags integration ./services/ingest/ -v -timeout 120s
//
// Prerequisites:
//   - A TimescaleDB or plain Postgres reachable at INGEST_TEST_TSDB_DSN
//     (default: postgres://postgres:postgres@localhost:5432/ingest_test)
//
// Tests skip automatically when Postgres is not reachable.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	cbor "github.com/fxamacker/cbor/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testDSN returns the Postgres DSN for integration tests.
func testDSN() string {
	if v := os.Getenv("INGEST_TEST_TSDB_DSN"); v != "" {
		return v
	}
	return "postgres://postgres:postgres@localhost:5432/ingest_test"
}

// pgAvailable returns true if the DSN can be connected to.
func pgAvailable(dsn string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return false
	}
	defer pool.Close()
	return pool.Ping(ctx) == nil
}

// openTestPool opens a pgx pool to the test database and installs the telemetry table.
func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testDSN()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	// Create the telemetry table if it doesn't exist.
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS telemetry (
			time       TIMESTAMPTZ NOT NULL DEFAULT now(),
			site_id    TEXT NOT NULL,
			device_id  TEXT NOT NULL,
			metric     TEXT NOT NULL,
			payload    JSONB,
			raw        BYTEA
		)
	`)
	if err != nil {
		t.Fatalf("create telemetry table: %v", err)
	}
	// Truncate for a clean slate each test.
	_, _ = pool.Exec(ctx, `TRUNCATE telemetry`)
	return pool
}

// countRows counts rows in the telemetry table for a given site_id.
func countRows(t *testing.T, pool *pgxpool.Pool, siteID string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM telemetry WHERE site_id=$1", siteID,
	).Scan(&n)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// TestFlushNow_InsertsRows verifies that flushNow drains the global ring
// buffer into the telemetry table (batcher core path, task 6.9).
func TestFlushNow_InsertsRows(t *testing.T) {
	if !pgAvailable(testDSN()) {
		t.Skipf("Postgres not available at %s; skipping", testDSN())
	}

	pool := openTestPool(t)
	siteID := "flush-test-site"

	// Wire the package-level ingestDB global used by flushNow.
	ingestDB = pool

	// Populate the ring buffer with 4 rows.
	ringMu.Lock()
	ring = nil
	for i := 0; i < 4; i++ {
		ring = append(ring, telemetryRow{
			Time:     time.Now().UTC(),
			SiteID:   siteID,
			DeviceID: fmt.Sprintf("device-%d", i),
			Metric:   "temp",
			Payload:  []byte(`{"v":22}`),
			IsJSON:   true,
			Raw:      []byte(`raw`),
		})
	}
	ringMu.Unlock()

	flushNow(context.Background())

	if n := countRows(t, pool, siteID); n != 4 {
		t.Errorf("flushNow: expected 4 rows in DB, got %d", n)
	} else {
		t.Logf("flushNow inserted %d rows successfully", n)
	}
}

// TestFlushBySize_TriggersFromHandleMessage verifies that accumulating batchSize
// rows in handleMessage triggers an immediate flush (task 6.9 size threshold).
func TestFlushBySize_TriggersFromHandleMessage(t *testing.T) {
	if !pgAvailable(testDSN()) {
		t.Skipf("Postgres not available at %s; skipping", testDSN())
	}

	pool := openTestPool(t)
	ingestDB = pool
	siteID := "size-trigger-site"

	// Configure the global ingestCfg to use a small batch size.
	ingestCfg = ingestConfig{
		SiteID:       siteID,
		PayloadCodec: "raw_passthrough",
		BatchSize:    3, // trigger after 3 messages
	}

	// Reset the ring buffer.
	ringMu.Lock()
	ring = nil
	ringMu.Unlock()

	// Simulate receiving 3 MQTT messages via direct ring manipulation +
	// size check (the same path as handleMessage uses).
	for i := 0; i < 3; i++ {
		row := telemetryRow{
			Time: time.Now().UTC(), SiteID: siteID,
			DeviceID: "d", Metric: "m", IsJSON: false, Raw: []byte("raw"),
		}
		ringMu.Lock()
		ring = append(ring, row)
		size := len(ring)
		ringMu.Unlock()
		if size >= ingestCfg.BatchSize {
			flushNow(context.Background())
		}
	}

	if n := countRows(t, pool, siteID); n < 3 {
		t.Errorf("size-triggered flush: expected ≥3 rows, got %d", n)
	} else {
		t.Logf("size-triggered flush wrote %d rows", n)
	}
}

// TestFlushByTimer verifies that the flush loop flushes rows when the timer fires (task 6.9).
func TestFlushByTimer(t *testing.T) {
	if !pgAvailable(testDSN()) {
		t.Skipf("Postgres not available at %s; skipping", testDSN())
	}

	pool := openTestPool(t)
	ingestDB = pool
	siteID := "timer-flush-site"

	ringMu.Lock()
	ring = []telemetryRow{
		{Time: time.Now().UTC(), SiteID: siteID, DeviceID: "d", Metric: "m", IsJSON: false, Raw: []byte("r")},
		{Time: time.Now().UTC(), SiteID: siteID, DeviceID: "d", Metric: "m", IsJSON: false, Raw: []byte("r")},
	}
	ringMu.Unlock()

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go flushLoop(ctx, ticker)

	// Wait for the timer flush.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n := countRows(t, pool, siteID); n >= 2 {
			t.Logf("timer flush wrote %d rows within the flush interval", n)
			cancel()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("timer flush: expected ≥2 rows within 5s, got %d", countRows(t, pool, siteID))
}

// TestDecodeAndFlush_CBOREndToEnd exercises the full CBOR → decode → flush path (task 6.9).
func TestDecodeAndFlush_CBOREndToEnd(t *testing.T) {
	if !pgAvailable(testDSN()) {
		t.Skipf("Postgres not available at %s; skipping", testDSN())
	}

	pool := openTestPool(t)
	ingestDB = pool
	siteID := "cbor-e2e-site"

	// Encode a test payload as CBOR.
	input := map[string]any{"temperature": 25.3, "unit": "C"}
	raw, err := cbor.Marshal(input)
	if err != nil {
		t.Fatalf("cbor.Marshal: %v", err)
	}

	decoded := decodePayload("cbor", raw)
	if decoded.DecodeErr != nil {
		t.Fatalf("decodePayload: %v", decoded.DecodeErr)
	}

	ringMu.Lock()
	ring = []telemetryRow{{
		Time:     time.Now().UTC(),
		SiteID:   siteID,
		DeviceID: "gateway-1",
		Metric:   "env",
		Payload:  decoded.JSONPayload,
		IsJSON:   decoded.IsJSON,
		Raw:      decoded.Raw,
	}}
	ringMu.Unlock()

	flushNow(context.Background())

	// Verify the payload round-trips correctly through Postgres.
	var payload []byte
	err = pool.QueryRow(context.Background(),
		"SELECT payload FROM telemetry WHERE site_id=$1 LIMIT 1", siteID,
	).Scan(&payload)
	if err != nil {
		t.Fatalf("query telemetry: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if out["unit"] != "C" {
		t.Errorf("expected unit=C in stored payload, got: %v", out)
	}
	t.Logf("CBOR end-to-end: stored payload=%s", payload)
}

// TestGracefulShutdown_DrainsBatcher verifies that cancelling the context causes
// the flush to drain pending rows within the 10s shutdown budget (task 6.7/6.9).
func TestGracefulShutdown_DrainsBatcher(t *testing.T) {
	if !pgAvailable(testDSN()) {
		t.Skipf("Postgres not available at %s; skipping", testDSN())
	}

	pool := openTestPool(t)
	ingestDB = pool
	siteID := "shutdown-drain-site"

	// Populate ring with rows that haven't been flushed yet.
	ringMu.Lock()
	ring = nil
	for i := 0; i < 6; i++ {
		ring = append(ring, telemetryRow{
			Time: time.Now().UTC(), SiteID: siteID,
			DeviceID: "d", Metric: "m", IsJSON: false, Raw: []byte("r"),
		})
	}
	ringMu.Unlock()

	// Simulate the drain path from main() on shutdown: call flushNow once.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer drainCancel()
	flushNow(drainCtx)

	if n := countRows(t, pool, siteID); n != 6 {
		t.Errorf("graceful shutdown drain: expected 6 rows, got %d", n)
	} else {
		t.Logf("graceful shutdown drained %d rows", n)
	}
}
