// Package recon implements the background reconciler loop described in
// design decision D11 and the reconciler capability spec.
package recon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"controlai/internal/audit"
	"controlai/internal/runner"
	"controlai/internal/store/sqlite"
)

// Config holds reconciler tuning parameters.
type Config struct {
	// DataDir is the root of /var/lib/controlai/.
	DataDir string
	// BasePeriod is the nominal loop interval (default 30 s).
	BasePeriod time.Duration
}

// backoffLadder defines the retry delay sequence (design D11).
var backoffLadder = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
}

// projectState tracks per-project backoff and failure counters.
type projectState struct {
	failureCount int
	nextRetry    time.Time
}

// Reconciler drives convergence between desired and actual compose state.
type Reconciler struct {
	cfg    Config
	store  *sqlite.Store
	docker *runner.DockerClient
	audit  audit.Emitter
	states map[string]*projectState // project_id → backoff state
	log    *slog.Logger
}

// New returns a new Reconciler.
func New(cfg Config, store *sqlite.Store, docker *runner.DockerClient, ae audit.Emitter, log *slog.Logger) *Reconciler {
	return &Reconciler{
		cfg:    cfg,
		store:  store,
		docker: docker,
		audit:  ae,
		states: map[string]*projectState{},
		log:    log,
	}
}

// Run starts the reconciler loop and blocks until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	period := r.cfg.BasePeriod
	if period <= 0 {
		period = 30 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	r.log.Info("reconciler started", "period", period)
	for {
		select {
		case <-ctx.Done():
			r.log.Info("reconciler stopping")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// Trigger can be called externally (e.g., after a controlai apply) to force
// an immediate reconciliation tick.
func (r *Reconciler) Trigger(ctx context.Context) {
	r.tick(ctx)
}

func (r *Reconciler) tick(ctx context.Context) {
	desiredList, err := r.store.ListDesiredStates(ctx)
	if err != nil {
		r.log.Error("reconciler: list desired states", "err", err)
		return
	}

	for _, desired := range desiredList {
		pid := desired.ProjectID
		ps, ok := r.states[pid]
		if !ok {
			ps = &projectState{}
			r.states[pid] = ps
		}
		// Skip if in backoff window.
		if !ps.nextRetry.IsZero() && time.Now().Before(ps.nextRetry) {
			continue
		}

		composeFile := r.composeFile(desired)
		if err := r.reconcileProject(ctx, desired, composeFile); err != nil {
			ps.failureCount++
			delay := backoffDelay(ps.failureCount)
			ps.nextRetry = time.Now().Add(delay)
			r.log.Warn("reconciler: project failed", "project", pid, "err", err, "next_retry", ps.nextRetry)
			_ = r.audit.Emit(ctx, audit.Event{
				Kind:     audit.KindReconcilerFailure,
				TenantID: desired.TenantID,
				SiteID:   desired.SiteID,
				Detail:   fmt.Sprintf(`{"err":%q,"backoff_s":%d}`, err.Error(), int(delay.Seconds())),
				Success:  false,
			})
			_ = r.audit.Emit(ctx, audit.Event{
				Kind:     audit.KindReconcilerBackoff,
				TenantID: desired.TenantID,
				SiteID:   desired.SiteID,
				Detail:   fmt.Sprintf(`{"next_retry":%q}`, ps.nextRetry.Format(time.RFC3339)),
				Success:  true,
			})
		} else {
			if ps.failureCount > 0 {
				r.log.Info("reconciler: project converged after failures", "project", pid)
			}
			// Reset backoff on success.
			ps.failureCount = 0
			ps.nextRetry = time.Time{}
		}
	}
}

// reconcileProject drives a single project toward desired state.
func (r *Reconciler) reconcileProject(ctx context.Context, desired sqlite.DesiredStateRow, composeFile string) error {
	switch desired.State {
	case "running":
		_, err := runner.Up(ctx, desired.ProjectID, composeFile)
		if err != nil {
			return err
		}
		_ = r.audit.Emit(ctx, audit.Event{
			Kind:     audit.KindReconcilerSuccess,
			TenantID: desired.TenantID,
			SiteID:   desired.SiteID,
			Detail:   `{"action":"up"}`,
			Success:  true,
		})
	case "stopped":
		_, err := runner.Down(ctx, desired.ProjectID, composeFile)
		if err != nil {
			return err
		}
		_ = r.audit.Emit(ctx, audit.Event{
			Kind:     audit.KindReconcilerSuccess,
			TenantID: desired.TenantID,
			SiteID:   desired.SiteID,
			Detail:   `{"action":"down"}`,
			Success:  true,
		})
	case "removed":
		_, err := runner.Down(ctx, desired.ProjectID, composeFile)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown desired state %q for project %s", desired.State, desired.ProjectID)
	}
	return nil
}

// composeFile returns the expected compose file path for a desired-state row.
func (r *Reconciler) composeFile(d sqlite.DesiredStateRow) string {
	if d.SiteID != "" {
		return filepath.Join(r.cfg.DataDir, "tenants", d.TenantID, "sites", d.SiteID, "docker-compose.yml")
	}
	if d.TenantID != "" {
		return filepath.Join(r.cfg.DataDir, "tenants", d.TenantID, "tsdb", "docker-compose.yml")
	}
	return filepath.Join(r.cfg.DataDir, "shared", "docker-compose.yml")
}

// backoffDelay returns the delay for the given failure count (1-indexed).
func backoffDelay(count int) time.Duration {
	if count <= 0 {
		return backoffLadder[0]
	}
	idx := count - 1
	if idx >= len(backoffLadder) {
		idx = len(backoffLadder) - 1
	}
	return backoffLadder[idx]
}
