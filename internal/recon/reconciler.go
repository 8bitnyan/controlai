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
	"controlai/internal/pki"
	"controlai/internal/render"
	"controlai/internal/runner"
	"controlai/internal/store/sqlite"
)

// Config holds reconciler tuning parameters.
type Config struct {
	// DataDir is the root of /var/lib/controlai/.
	DataDir string
	// BasePeriod is the nominal loop interval (default 30 s).
	BasePeriod time.Duration
	// LastTick is an optional pointer updated after every tick for the health endpoint.
	LastTick *time.Time
	// MasterKey is the AES-256-GCM master key for PKI operations; nil disables PKI rotation.
	MasterKey []byte
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
	cfg      Config
	store    *sqlite.Store
	docker   *runner.DockerClient
	audit    audit.Emitter
	renderer *render.Renderer
	states   map[string]*projectState // project_id → backoff state
	log      *slog.Logger
}

// New returns a new Reconciler.
func New(cfg Config, store *sqlite.Store, docker *runner.DockerClient, ae audit.Emitter, log *slog.Logger) *Reconciler {
	return &Reconciler{
		cfg:      cfg,
		store:    store,
		docker:   docker,
		audit:    ae,
		renderer: render.New(),
		states:   map[string]*projectState{},
		log:      log,
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

	// Run PKI EnsureTrustFiles for every active site on this tick.
	if len(r.cfg.MasterKey) > 0 {
		r.ensureAllTrustFiles(ctx)
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

	// Update last-tick timestamp for the health endpoint.
	if r.cfg.LastTick != nil {
		*r.cfg.LastTick = time.Now()
	}
}

// reconcileProject drives a single project toward desired state.
func (r *Reconciler) reconcileProject(ctx context.Context, desired sqlite.DesiredStateRow, composeFile string) error {
	switch desired.State {
	case "running":
		// Check actual state via Docker SDK before running compose up.
		// Only call up if containers are missing or any are not running.
		needsUp := true
		if r.docker != nil {
			containers, err := r.docker.ListByProject(ctx, desired.ProjectID)
			if err == nil && len(containers) > 0 {
				allRunning := true
				for _, c := range containers {
					if c.State != "running" {
						allRunning = false
						break
					}
				}
				if allRunning {
					// Everything is already running — no action needed this tick.
					needsUp = false
				}
			}
		}
		if !needsUp {
			return nil
		}
		// Write Traefik dynamic config before bringing up the site so that the
		// SNI route is present by the time the broker is reachable.
		if desired.SiteID != "" && desired.TenantID != "" {
			if err := r.ensureTraefikDynamic(ctx, desired.TenantID, desired.SiteID); err != nil {
				r.log.Warn("traefik dynamic config write failed; continuing reconcile", "err", err)
			}
		}
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
		// Check if containers are already stopped/absent before running down.
		if r.docker != nil {
			containers, err := r.docker.ListByProject(ctx, desired.ProjectID)
			if err == nil && len(containers) == 0 {
				return nil // nothing running, desired=stopped is already satisfied
			}
		}
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

// ensureAllTrustFiles runs PKI rotation checks for every active site.
// Rotation is triggered when any cert is: missing, fails CA verification,
// expires within 30 days, or (server cert) has SAN drift.
func (r *Reconciler) ensureAllTrustFiles(ctx context.Context) {
	sites, err := r.store.ListAllSites(ctx)
	if err != nil {
		r.log.Warn("reconciler: list sites for PKI check failed", "err", err)
		return
	}
	for _, site := range sites {
		if site.Status != "active" {
			continue
		}
		tenant, err := r.store.GetTenant(ctx, site.TenantID)
		if err != nil {
			continue
		}
		certDir := filepath.Join(r.cfg.DataDir, "tenants", site.TenantID, "sites", site.ID, "deploy", "certs", "active")
		caRow, err := r.store.GetCACert(ctx, site.ID)
		if err != nil {
			// No CA yet — skip (CA is created at site provisioning time).
			continue
		}
		sniHostname := site.ID + "." + site.TenantID + "." + tenant.Domain
		cfg := pki.TrustFileConfig{
			SiteID:      site.ID,
			TenantID:    site.TenantID,
			CertDir:     certDir,
			SNIHostname: sniHostname,
			MasterKey:   r.cfg.MasterKey,
			CACertPEM:   []byte(caRow.CertPEM),
			CAKeyEnc:    caRow.KeyEnc,
			CAKeyNonce:  caRow.KeyNonce,
		}
		result, err := pki.EnsureTrustFiles(cfg)
		if err != nil {
			r.log.Warn("reconciler: PKI EnsureTrustFiles failed", "site", site.ID, "err", err)
			continue
		}
		if result.ServerRotated || result.IngestorRotated {
			r.log.Info("reconciler: PKI certs rotated", "site", site.ID,
				"server_rotated", result.ServerRotated, "ingestor_rotated", result.IngestorRotated)
			_ = r.audit.Emit(ctx, audit.Event{
				Kind:     audit.KindPKIRotate,
				TenantID: site.TenantID,
				SiteID:   site.ID,
				Detail:   fmt.Sprintf(`{"server_rotated":%t,"ingestor_rotated":%t}`, result.ServerRotated, result.IngestorRotated),
				Success:  true,
			})
			// For EMQX sites, the broker listener needs restarting after cert rotation.
			// The restart is done via EMQX REST API; in MVP we rely on container restart.
			if site.BrokerKind == "emqx" && result.ServerRotated {
				r.log.Info("reconciler: triggering compose restart for EMQX after cert rotation", "site", site.ID)
				composeFile := filepath.Join(r.cfg.DataDir, "tenants", site.TenantID, "sites", site.ID, "docker-compose.yml")
				if _, err := runner.Restart(ctx, site.TenantID+"-"+site.ID, composeFile, "broker"); err != nil {
					r.log.Warn("reconciler: EMQX broker restart failed", "site", site.ID, "err", err)
					_ = r.audit.Emit(ctx, audit.Event{
						Kind:     audit.KindBrokerRestart,
						TenantID: site.TenantID,
						SiteID:   site.ID,
						Detail:   fmt.Sprintf(`{"err":%q}`, err.Error()),
						Success:  false,
					})
				} else {
					_ = r.audit.Emit(ctx, audit.Event{
						Kind:     audit.KindBrokerRestart,
						TenantID: site.TenantID,
						SiteID:   site.ID,
						Detail:   `{}`,
						Success:  true,
					})
				}
			}
		}
	}
}

// ensureInitialTrustFiles is called at daemon start for all active sites.
// It is a thin wrapper around ensureAllTrustFiles.
func (r *Reconciler) EnsureInitialTrustFiles(ctx context.Context) {
	if len(r.cfg.MasterKey) > 0 {
		r.ensureAllTrustFiles(ctx)
	}
}

// ListByProjectOrNil safely lists containers; returns nil on error.
func listByProjectOrNil(ctx context.Context, docker *runner.DockerClient, projectID string) []runner.ContainerSummary {
	if docker == nil {
		return nil
	}
	cs, err := docker.ListByProject(ctx, projectID)
	if err != nil {
		return nil
	}
	return cs
}

// IsConverged returns true when all containers in the given project are running.
// Returns (false, nil) when docker is nil or the project has no containers.
func (r *Reconciler) IsConverged(ctx context.Context, projectID string) (bool, error) {
	if r.docker == nil {
		return false, nil
	}
	containers, err := r.docker.ListByProject(ctx, projectID)
	if err != nil {
		return false, fmt.Errorf("list containers for %s: %w", projectID, err)
	}
	if len(containers) == 0 {
		return false, nil
	}
	for _, c := range containers {
		if c.State != "running" {
			return false, nil
		}
	}
	return true, nil
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

// ensureTraefikDynamic renders the per-site Traefik dynamic config and writes it
// atomically to the shared dynamic directory. Called before bringing a site up.
func (r *Reconciler) ensureTraefikDynamic(ctx context.Context, tenantID, siteID string) error {
	tenant, err := r.store.GetTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("get tenant %s: %w", tenantID, err)
	}
	site, err := r.store.GetSite(ctx, siteID)
	if err != nil {
		return fmt.Errorf("get site %s: %w", siteID, err)
	}

	sniHostname := siteID + "." + tenantID + "." + tenant.Domain
	rctx := render.RenderContext{
		Tenant: render.TenantCtx{
			ID:     tenant.ID,
			Domain: tenant.Domain,
		},
		Site: &render.SiteCtx{
			ID:          site.ID,
			TenantID:    site.TenantID,
			BrokerKind:  site.BrokerKind,
			SNIHostname: sniHostname,
		},
	}
	results, err := r.renderer.RenderTraefikDynamicForSite(rctx)
	if err != nil {
		return fmt.Errorf("render traefik dynamic for %s/%s: %w", tenantID, siteID, err)
	}
	dynamicDir := filepath.Join(r.cfg.DataDir, "shared", "traefik", "dynamic")
	if err := render.WriteResults(dynamicDir, results); err != nil {
		return fmt.Errorf("write traefik dynamic for %s/%s: %w", tenantID, siteID, err)
	}
	r.log.Info("traefik dynamic config updated", "tenant", tenantID, "site", siteID,
		"sni", sniHostname, "files", len(results))
	return nil
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
