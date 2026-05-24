// Package daemon implements the controlai REST API server on two transports:
// a unix domain socket (default) and an optional TCP+TLS listener.
package daemon

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	autopaho "github.com/eclipse/paho.golang/autopaho"
	pahopkg "github.com/eclipse/paho.golang/paho"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"controlai/internal/audit"
	"controlai/internal/backup"
	"controlai/internal/capacity"
	"controlai/internal/pki"
	"controlai/internal/recon"
	"controlai/internal/render"
	"controlai/internal/store/sqlite"
	"controlai/internal/version"
)

// Config holds daemon server configuration.
type Config struct {
	// SocketPath is the unix socket path (default /run/controlai/controlai.sock).
	SocketPath string
	// TCPAddr is the optional TCP listen address (e.g. ":8443"). Empty = disabled.
	TCPAddr string
	// TLSCertFile and TLSKeyFile are used for the TCP listener.
	TLSCertFile string
	TLSKeyFile  string
	// DataDir is /var/lib/controlai/.
	DataDir string
	// DevMode disables master-key requirement.
	DevMode bool
	// ReconcilerLastTick is a pointer updated by the reconciler for health checks.
	ReconcilerLastTick *time.Time
	// DockerReachable is a function the health handler calls.
	DockerReachable func(ctx context.Context) bool
	// StartedAt is the daemon start timestamp.
	StartedAt time.Time
	// DockerListByProject lists containers for a project via the Docker SDK.
	// Used by the blocking apply handler to poll for convergence.
	DockerListByProject func(ctx context.Context, projectID string) ([]ContainerState, error)
	// MasterKey is the AES-256-GCM master key for PKI operations.
	// Empty slice disables automatic CA generation.
	MasterKey []byte
}

// ContainerState is a minimal view of a Docker container's state.
type ContainerState struct {
	Name  string
	State string // "running" | "exited" | etc.
}

// Server is the REST API server.
type Server struct {
	cfg      Config
	store    *sqlite.Store
	audit    audit.Emitter
	recon    *recon.Reconciler
	renderer *render.Renderer
	router   chi.Router
	log      *slog.Logger
}

// New constructs a new Server.
func New(cfg Config, store *sqlite.Store, ae audit.Emitter, rec *recon.Reconciler, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: store, audit: ae, recon: rec, renderer: render.New(), log: log}
	s.router = s.buildRouter()
	return s
}

// Handler returns the raw http.Handler for the unix-socket transport (no auth).
// Useful for testing without spinning up a real listener.
func (s *Server) Handler() http.Handler { return s.router }

// TCPHandler returns the http.Handler for the TCP transport (bearer-token required).
// Useful for testing the auth middleware without a real TLS listener.
func (s *Server) TCPHandler() http.Handler { return s.tokenAuthMiddleware(s.router) }

// ServeUnix starts the unix socket listener. Blocks until ctx is done.
func (s *Server) ServeUnix(ctx context.Context) error {
	_ = os.Remove(s.cfg.SocketPath)
	ln, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", s.cfg.SocketPath, err)
	}
	_ = os.Chmod(s.cfg.SocketPath, 0o660)
	s.log.Info("daemon listening on unix socket", "path", s.cfg.SocketPath)
	srv := &http.Server{Handler: s.router}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ServeTCP starts the optional TLS TCP listener. Blocks until ctx is done.
func (s *Server) ServeTCP(ctx context.Context) error {
	if s.cfg.TCPAddr == "" {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("load TLS keypair: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	ln, err := tls.Listen("tcp", s.cfg.TCPAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("listen TCP %s: %w", s.cfg.TCPAddr, err)
	}
	s.log.Info("daemon listening on TCP+TLS", "addr", s.cfg.TCPAddr)
	srv := &http.Server{Handler: s.tokenAuthMiddleware(s.router)}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// buildRouter constructs the chi router with all /v1/ handlers.
func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Get("/v1/health", s.handleHealth)
	r.Get("/v1/capacity", s.handleCapacity)

	// Tenants
	r.Post("/v1/tenants", s.handleCreateTenant)
	r.Get("/v1/tenants", s.handleListTenants)
	r.Get("/v1/tenants/{tid}", s.handleGetTenant)
	r.Patch("/v1/tenants/{tid}", s.handleUpdateTenant)
	r.Delete("/v1/tenants/{tid}", s.handleDeleteTenant)

	// Sites
	r.Post("/v1/tenants/{tid}/sites", s.handleCreateSite)
	r.Get("/v1/tenants/{tid}/sites", s.handleListSites)
	r.Get("/v1/tenants/{tid}/sites/{sid}", s.handleGetSite)
	r.Patch("/v1/tenants/{tid}/sites/{sid}", s.handleUpdateSite)
	r.Delete("/v1/tenants/{tid}/sites/{sid}", s.handleDeleteSite)

	// Lifecycle stop/start for tenants and sites
	r.Post("/v1/tenants/{tid}/start", s.handleStartTenant)
	r.Post("/v1/tenants/{tid}/stop", s.handleStopTenant)
	r.Post("/v1/tenants/{tid}/sites/{sid}/start", s.handleStartSite)
	r.Post("/v1/tenants/{tid}/sites/{sid}/stop", s.handleStopSite)

	// Bi-mode downlink
	r.Post("/v1/tenants/{tid}/sites/{sid}/publish", s.handlePublish)

	// Logs
	r.Get("/v1/tenants/{tid}/sites/{sid}/logs", s.handleLogs)

	// Apply
	r.Post("/v1/apply/{selector}", s.handleApply)

	// Status overview (tenant list with statuses)
	r.Get("/v1/status", s.handleStatus)

	// PKI
	r.Post("/v1/tenants/{tid}/sites/{sid}/pki/certs", s.handlePKIIssueCert)
	r.Delete("/v1/tenants/{tid}/sites/{sid}/pki/certs/{fp}", s.handlePKIRevokeCert)

	return r
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dockerOK := false
	if s.cfg.DockerReachable != nil {
		dockerOK = s.cfg.DockerReachable(r.Context())
	}
	var lastTick string
	if s.cfg.ReconcilerLastTick != nil && !s.cfg.ReconcilerLastTick.IsZero() {
		lastTick = s.cfg.ReconcilerLastTick.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "ok",
		"version":             version.Version,
		"docker_reachable":    dockerOK,
		"registry_healthy":    s.store != nil,
		"reconciler_last_tick": lastTick,
	})
}

func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var plan []capacity.TenantPlan
	for _, t := range tenants {
		sites, err := s.store.ListSitesByTenant(ctx, t.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		tp := capacity.TenantPlan{TenantID: t.ID}
		for _, site := range sites {
			tp.Sites = append(tp.Sites, capacity.SitePlan{
				SiteID:     site.ID,
				BrokerKind: site.BrokerKind,
				Tier:       site.Throughput,
				Direction:  site.Direction,
			})
		}
		plan = append(plan, tp)
	}
	memKB, err := capacity.ReadMemTotalKB()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	pred, err := capacity.Predict(plan, memKB)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, pred)
}

func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug      string `json:"slug"`
		Name      string `json:"name"`
		Domain    string `json:"domain"`
		Retention string `json:"retention"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if err := validateSlugHTTP(w, req.Slug); err != nil {
		return
	}
	if req.ProjectID != "" {
		if err := validateProjectIDHTTP(w, req.ProjectID); err != nil {
			return
		}
	}
	if req.Retention == "" {
		req.Retention = "7d"
	}
	tenantID := "tnt_" + req.Slug
	// Admission check
	if err := s.admissionCheck(r.Context(), tenantID, nil); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	err := s.store.CreateTenant(r.Context(), sqlite.TenantRow{
		ID:            tenantID,
		Name:          req.Name,
		Domain:        req.Domain,
		Retention:     req.Retention,
		SchemaVersion: 1,
		ProjectID:     req.ProjectID,
	})
	if err != nil {
		if err == sqlite.ErrDuplicate {
			writeErr(w, http.StatusConflict, fmt.Errorf("tenant %q already exists", tenantID))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Render TSDB compose project and write to disk. The reconciler needs the
	// compose file to exist before it can bring up containers.
	if _, err := renderAndWriteTSDB(r.Context(), tenantID, s.store, s.renderer, s.cfg.DataDir); err != nil {
		// Non-fatal: log and continue — the operator can re-trigger via apply.
		s.log.Warn("render TSDB on create failed", "tenant", tenantID, "err", err)
	} else if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}

	// Best-effort: install the daily backup systemd timer for this tenant.
	// Non-fatal on non-Linux or environments without systemd.
	installBackupTimer(tenantID, s.cfg.DevMode, s.log)

	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindTenantCreate, TenantID: tenantID, ProjectID: req.ProjectID, Success: true})
	out := map[string]string{"id": tenantID}
	if req.ProjectID != "" {
		out["project_id"] = req.ProjectID
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	var projectIDFilter *string
	if v, ok := r.URL.Query()["project_id"]; ok {
		// Query param present (even if empty).
		filter := v[0]
		// Validate non-empty values; empty string means "untagged" which is valid.
		if filter != "" {
			if err := validateProjectIDHTTP(w, filter); err != nil {
				return
			}
		}
		projectIDFilter = &filter
	}
	rows, err := s.store.ListTenantsFiltered(r.Context(), projectIDFilter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	row, err := s.store.GetTenant(r.Context(), tid)
	if err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("tenant %q not found", tid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	var req struct {
		Retention *string `json:"retention"`
		Name      string  `json:"name"`
		Domain    string  `json:"domain"`
		ProjectID *string `json:"project_id"` // nil = not in payload; ptr to "" = clear; ptr to val = set
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	// Validate retention if provided.
	if req.Retention != nil && *req.Retention != "" {
		validRet := map[string]bool{"1m": true, "1h": true, "1d": true, "7d": true, "30d": true}
		if !validRet[*req.Retention] {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid retention %q: must be one of 1m 1h 1d 7d 30d", *req.Retention))
			return
		}
		if err := s.store.UpdateTenantRetention(r.Context(), tid, *req.Retention); err != nil {
			if err == sqlite.ErrNotFound {
				writeErr(w, http.StatusNotFound, fmt.Errorf("tenant %q not found", tid))
				return
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Update project_id if present in payload.
	if req.ProjectID != nil {
		pid := *req.ProjectID
		if pid != "" {
			if err := validateProjectIDHTTP(w, pid); err != nil {
				return
			}
		}
		if err := s.store.UpdateTenantProjectID(r.Context(), tid, pid); err != nil {
			if err == sqlite.ErrNotFound {
				writeErr(w, http.StatusNotFound, fmt.Errorf("tenant %q not found", tid))
				return
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Re-render TSDB init.sql and related files so the retention policy change
	// propagates to the compose project on the next reconciler tick.
	if req.Retention != nil && *req.Retention != "" {
		if _, err := renderAndWriteTSDB(r.Context(), tid, s.store, s.renderer, s.cfg.DataDir); err != nil {
			s.log.Warn("re-render TSDB after retention change failed", "tenant", tid, "err", err)
		} else if s.recon != nil {
			go s.recon.Trigger(context.Background())
		}
	}

	// Fetch updated tenant to include in response and audit.
	tenant, err := s.store.GetTenant(r.Context(), tid)
	if err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("tenant %q not found", tid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	retVal := ""
	if req.Retention != nil {
		retVal = *req.Retention
	}
	_ = s.audit.Emit(r.Context(), audit.Event{
		Kind:      audit.KindTenantUpdate,
		TenantID:  tid,
		ProjectID: tenant.ProjectID,
		Detail:    fmt.Sprintf(`{"retention":%q}`, retVal),
		Success:   true,
	})
	writeJSON(w, http.StatusOK, tenant)
}

func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	purge := r.URL.Query().Get("purge") == "true"
	// Look up project_id before deleting for audit log enrichment.
	pid := s.tenantProjectID(r.Context(), tid)

	// Set desired_state=stopped for the TSDB project and all sites before
	// changing SQLite status, so the reconciler stops containers on the next tick.
	// With purge=true, we use "removed" so the reconciler also tears down volumes
	// (volume removal is handled separately; "removed" stops the project).
	desiredState := "stopped"
	if purge {
		desiredState = "removed"
	}
	_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
		ProjectID: tid + "-tsdb", TenantID: tid, State: desiredState,
	})
	// Stop all sites under this tenant.
	if sites, err := s.store.ListSitesByTenant(r.Context(), tid); err == nil {
		for _, site := range sites {
			_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
				ProjectID: tid + "-" + site.ID, TenantID: tid, SiteID: site.ID,
				State: desiredState,
			})
		}
	}
	if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}

	if err := s.store.DeleteTenant(r.Context(), tid, purge); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindTenantDelete, TenantID: tid,
		ProjectID: pid, Detail: fmt.Sprintf(`{"purge":%t}`, purge), Success: true})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	// Check tenant exists
	if _, err := s.store.GetTenant(r.Context(), tid); err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("tenant %q not found", tid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var req struct {
		Slug         string `json:"slug"`
		BrokerKind   string `json:"broker_kind"`
		Throughput   string `json:"throughput"`
		Direction    string `json:"direction"`
		PayloadCodec string `json:"payload_codec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if err := validateSlugHTTP(w, req.Slug); err != nil {
		return
	}
	if req.BrokerKind == "" {
		req.BrokerKind = "mosquitto"
	}
	if req.Throughput == "" {
		req.Throughput = "low"
	}
	if req.Direction == "" {
		req.Direction = "uni"
	}
	if req.PayloadCodec == "" {
		req.PayloadCodec = "cbor"
	}
	// High tier check
	if req.Throughput == "high" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("high tier requires t3.large or larger; use low or mid"))
		return
	}
	// Capability matrix check
	if req.BrokerKind == "mosquitto" && req.Throughput == "mid" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("mosquitto + mid throughput requires 2 replicas "+
			"but mosquitto lacks shared subscriptions; use emqx or low throughput"))
		return
	}
	siteID := "ste_" + req.Slug
	// Admission check
	if err := s.admissionCheck(r.Context(), tid, &sqlite.SiteRow{
		BrokerKind: req.BrokerKind, Throughput: req.Throughput, Direction: req.Direction,
	}); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	err := s.store.CreateSite(r.Context(), sqlite.SiteRow{
		ID: siteID, TenantID: tid,
		BrokerKind: req.BrokerKind, Throughput: req.Throughput, Direction: req.Direction,
		PayloadCodec: req.PayloadCodec, LeafTTLDays: 365, SchemaVersion: 1,
	})
	if err != nil {
		if err == sqlite.ErrDuplicate {
			writeErr(w, http.StatusConflict, fmt.Errorf("site %q already exists under tenant %q", siteID, tid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Generate per-site CA if master key is available. The CA is stored in
	// SQLite and used by the reconciler's EnsureTrustFiles to provision certs.
	if len(s.cfg.MasterKey) > 0 {
		if _, caErr := s.store.GetCACert(r.Context(), siteID); caErr == sqlite.ErrNotFound {
			ca, caGenErr := pki.GenerateCA(tid, siteID, s.cfg.MasterKey)
			if caGenErr != nil {
				s.log.Warn("generate site CA failed", "site", siteID, "err", caGenErr)
			} else {
				caRow := sqlite.CACertRow{
					SiteID:      siteID,
					CertPEM:     string(ca.CertPEM),
					KeyEnc:      ca.KeyEncrypted,
					KeyNonce:    ca.KeyNonce,
					Fingerprint: ca.Fingerprint,
					NotBefore:   time.Now().Add(-5 * time.Minute),
					NotAfter:    time.Now().AddDate(20, 0, 0),
				}
				if storeErr := s.store.StoreCACert(r.Context(), caRow); storeErr != nil {
					s.log.Warn("store site CA failed", "site", siteID, "err", storeErr)
				} else {
					s.log.Info("per-site CA generated and stored", "site", siteID, "fingerprint", ca.Fingerprint)
				}
			}
		}
	}

	// Render broker+ingest compose project and write to disk so the reconciler
	// can converge immediately on the next tick.
	if _, err := renderAndWriteSite(r.Context(), siteID, s.store, s.renderer, s.cfg.DataDir); err != nil {
		s.log.Warn("render site on create failed", "site", siteID, "err", err)
	} else if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}

	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindSiteCreate, TenantID: tid, SiteID: siteID, ProjectID: s.tenantProjectID(r.Context(), tid), Success: true})
	writeJSON(w, http.StatusCreated, map[string]string{"id": siteID})
}

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	rows, err := s.store.ListSitesByTenant(r.Context(), tid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleGetSite(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	row, err := s.store.GetSite(r.Context(), sid)
	if err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("site %q not found", sid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleUpdateSite(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	sid := chi.URLParam(r, "sid")
	var req struct {
		PayloadCodec string `json:"payload_codec"`
		Direction    string `json:"direction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}

	// Validate direction if provided.
	if req.Direction != "" {
		if req.Direction != "uni" && req.Direction != "bi" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid direction %q: must be uni or bi", req.Direction))
			return
		}
	}
	// Validate codec if provided.
	if req.PayloadCodec != "" {
		validCodecs := map[string]bool{"cbor": true, "json": true, "raw_passthrough": true}
		if !validCodecs[req.PayloadCodec] {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid payload_codec %q: must be cbor, json, or raw_passthrough", req.PayloadCodec))
			return
		}
	}

	if req.Direction == "" && req.PayloadCodec == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("at least one of payload_codec or direction must be specified"))
		return
	}

	// Apply the update.
	if err := s.store.UpdateSite(r.Context(), sid, req.PayloadCodec, req.Direction); err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("site %q not found", sid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Re-render site compose files so the reconciler detects the hash change
	// and restarts only the affected containers.
	if _, err := renderAndWriteSite(r.Context(), sid, s.store, s.renderer, s.cfg.DataDir); err != nil {
		s.log.Warn("re-render site after update failed", "site", sid, "err", err)
	} else if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}

	_ = s.audit.Emit(r.Context(), audit.Event{
		Kind: audit.KindSiteUpdate, TenantID: tid, SiteID: sid,
		ProjectID: s.tenantProjectID(r.Context(), tid),
		Detail:    fmt.Sprintf(`{"codec":%q,"direction":%q}`, req.PayloadCodec, req.Direction),
		Success:   true,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	tid := chi.URLParam(r, "tid")
	purge := r.URL.Query().Get("purge") == "true"

	// Set desired_state=stopped (or removed on purge) before deleting from SQLite
	// so the reconciler stops the site's containers on the next tick.
	desiredState := "stopped"
	if purge {
		desiredState = "removed"
	}
	_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
		ProjectID: tid + "-" + sid, TenantID: tid, SiteID: sid, State: desiredState,
	})
	if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}

	if err := s.store.DeleteSite(r.Context(), sid, purge); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindSiteDelete, TenantID: tid, SiteID: sid,
		ProjectID: s.tenantProjectID(r.Context(), tid),
		Detail: fmt.Sprintf(`{"purge":%t}`, purge), Success: true})
	w.WriteHeader(http.StatusNoContent)
}

// ─── Lifecycle stop/start ─────────────────────────────────────────────────────

func (s *Server) handleStartTenant(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	tenant, err := s.store.GetTenant(r.Context(), tid)
	if err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("tenant %q not found", tid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
		ProjectID: tid + "-tsdb", TenantID: tid, State: "running",
	})
	_ = s.store.UpdateTenantStatus(r.Context(), tid, "active")
	if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindTenantUpdate, TenantID: tid,
		ProjectID: tenant.ProjectID, Detail: `{"action":"start"}`, Success: true})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "starting"})
}

func (s *Server) handleStopTenant(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	tenant, err := s.store.GetTenant(r.Context(), tid)
	if err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("tenant %q not found", tid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
		ProjectID: tid + "-tsdb", TenantID: tid, State: "stopped",
	})
	// Also stop all sites under this tenant.
	if sites, err := s.store.ListSitesByTenant(r.Context(), tid); err == nil {
		for _, site := range sites {
			_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
				ProjectID: tid + "-" + site.ID, TenantID: tid, SiteID: site.ID, State: "stopped",
			})
		}
	}
	if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindTenantUpdate, TenantID: tid,
		ProjectID: tenant.ProjectID, Detail: `{"action":"stop"}`, Success: true})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

func (s *Server) handleStartSite(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	sid := chi.URLParam(r, "sid")
	if _, err := s.store.GetSite(r.Context(), sid); err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("site %q not found", sid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
		ProjectID: tid + "-" + sid, TenantID: tid, SiteID: sid, State: "running",
	})
	if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindSiteStart, TenantID: tid, SiteID: sid,
		ProjectID: s.tenantProjectID(r.Context(), tid), Success: true})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "starting"})
}

func (s *Server) handleStopSite(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	sid := chi.URLParam(r, "sid")
	if _, err := s.store.GetSite(r.Context(), sid); err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("site %q not found", sid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
		ProjectID: tid + "-" + sid, TenantID: tid, SiteID: sid, State: "stopped",
	})
	if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindSiteStop, TenantID: tid, SiteID: sid,
		ProjectID: s.tenantProjectID(r.Context(), tid), Success: true})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

// handleStatus returns a summary of all tenants with their status and site counts.
// This backs `controlai status`.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	tenants, err := s.store.ListTenants(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type tenantStatus struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Status    string `json:"status"`
		Retention string `json:"retention"`
		SiteCount int    `json:"site_count"`
	}
	out := make([]tenantStatus, 0, len(tenants))
	for _, t := range tenants {
		sites, _ := s.store.ListSitesByTenant(r.Context(), t.ID)
		out = append(out, tenantStatus{
			ID:        t.ID,
			Name:      t.Name,
			Status:    t.Status,
			Retention: t.Retention,
			SiteCount: len(sites),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	sid := chi.URLParam(r, "sid")
	site, err := s.store.GetSite(r.Context(), sid)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("site %q not found", sid))
		return
	}
	if site.Direction != "bi" {
		writeErr(w, http.StatusConflict, fmt.Errorf("site %s/%s is direction=uni; downlink publish not supported", tid, sid))
		return
	}
	var req struct {
		Topic   string `json:"topic"`
		Payload string `json:"payload"`
		QoS     int    `json:"qos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Topic == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("topic is required"))
		return
	}
	if req.QoS < 0 || req.QoS > 2 {
		req.QoS = 1
	}

	// Get tenant to look up the domain for SNI routing.
	tenant, err := s.store.GetTenant(r.Context(), tid)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("tenant %q not found", tid))
		return
	}

	// The daemon publishes directly to the broker via Traefik :8883 with
	// SNI routing, using the site's ingestor client certificate.
	sniHost := sid + "." + tid + "." + tenant.Domain
	certDir := fmt.Sprintf("%s/tenants/%s/sites/%s/deploy/certs/active", s.cfg.DataDir, tid, sid)
	publishCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := s.mqttPublish(publishCtx, sniHost, certDir, req.Topic, req.Payload, byte(req.QoS)); err != nil {
		s.log.Warn("downlink publish failed", "site", sid, "err", err)
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("downlink publish failed: %w", err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// mqttPublish publishes a single MQTT message to the given SNI-routed broker
// on :8883 using the site's ingestor client certificate. The connection is
// created, used, and closed within the provided context.
func (s *Server) mqttPublish(ctx context.Context, sniHost, certDir, topic, payload string, qos byte) error {
	// Load CA, client cert, and key.
	caPEM, err := os.ReadFile(certDir + "/ca.crt")
	if err != nil {
		return fmt.Errorf("read CA: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("parse CA cert")
	}
	clientCert, err := tls.LoadX509KeyPair(certDir+"/ingestor.crt", certDir+"/ingestor.key")
	if err != nil {
		return fmt.Errorf("load ingestor cert: %w", err)
	}
	tlsCfg := &tls.Config{
		RootCAs:      caPool,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   sniHost,
		MinVersion:   tls.VersionTLS12,
	}

	brokerURL, _ := url.Parse("mqtts://localhost:8883")
	published := make(chan error, 1)
	pahoConnCfg := autopaho.ClientConfig{
		BrokerUrls:        []*url.URL{brokerURL},
		TlsCfg:            tlsCfg,
		KeepAlive:         5,
		ConnectRetryDelay: 500 * time.Millisecond,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *pahopkg.Connack) {
			pubCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := cm.Publish(pubCtx, &pahopkg.Publish{
				Topic:   topic,
				Payload: []byte(payload),
				QoS:     qos,
			}); err != nil {
				published <- fmt.Errorf("publish: %w", err)
				return
			}
			published <- nil
			_ = cm.Disconnect(pubCtx)
		},
		OnConnectError: func(err error) {
			select {
			case published <- fmt.Errorf("connect: %w", err):
			default:
			}
		},
		ClientConfig: pahopkg.ClientConfig{
			ClientID: fmt.Sprintf("controlai-daemon-downlink-%d", time.Now().UnixNano()),
		},
	}

	cm, err := autopaho.NewConnection(ctx, pahoConnCfg)
	if err != nil {
		return fmt.Errorf("create MQTT connection: %w", err)
	}
	defer func() { _ = cm.Disconnect(context.Background()) }()

	select {
	case err := <-published:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	sid := chi.URLParam(r, "sid")

	// Validate the site exists.
	if _, err := s.store.GetSite(r.Context(), sid); err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("site %q not found", sid))
		return
	}

	service := r.URL.Query().Get("service")
	tailStr := r.URL.Query().Get("tail")
	tail := 100
	if tailStr != "" {
		if n, err := strconv.Atoi(tailStr); err == nil && n > 0 {
			tail = n
		}
	}
	if tail > 1000 {
		tail = 1000 // cap to prevent accidental huge responses
	}

	projectID := tid + "-" + sid
	composeFile := fmt.Sprintf("%s/tenants/%s/sites/%s/docker-compose.yml", s.cfg.DataDir, tid, sid)

	// Build docker compose logs command.
	args := []string{"compose", "-p", projectID, "-f", composeFile,
		"logs", "--no-color", "--tail", strconv.Itoa(tail)}
	if service != "" {
		args = append(args, service)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		// docker compose exits non-zero when the project doesn't exist yet.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out.Bytes())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Bytes())
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	selector := chi.URLParam(r, "selector")
	// Write desired=running for the selector and trigger reconciler.
	_ = s.store.UpsertDesiredState(r.Context(), sqlite.DesiredStateRow{
		ProjectID: selector,
		State:     "running",
	})
	if s.recon != nil {
		go s.recon.Trigger(context.Background())
	}

	// Optional blocking wait: ?wait=<seconds> (max 300).
	// When provided, the handler polls until all containers are running or timeout.
	if waitStr := r.URL.Query().Get("wait"); waitStr != "" {
		waitSecs, _ := strconv.Atoi(waitStr)
		if waitSecs > 0 {
			if waitSecs > 300 {
				waitSecs = 300
			}
			deadline := time.Now().Add(time.Duration(waitSecs) * time.Second)
			s.pollConvergence(r.Context(), w, selector, deadline)
			return
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"selector": selector, "status": "applying"})
}

// pollConvergence polls the Docker SDK until all containers for projectID are
// running (converged) or the deadline elapses. It writes either a 200 OK with
// converged:true or a 202 Accepted with converged:false and the last mismatch reason.
func (s *Server) pollConvergence(ctx context.Context, w http.ResponseWriter, projectID string, deadline time.Time) {
	if s.cfg.DockerListByProject == nil {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"selector":  projectID,
			"converged": false,
			"reason":    "docker SDK not available",
		})
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastReason string
	for {
		containers, err := s.cfg.DockerListByProject(ctx, projectID)
		if err == nil && len(containers) > 0 {
			allRunning := true
			for _, c := range containers {
				if c.State != "running" {
					allRunning = false
					lastReason = fmt.Sprintf("container %s is in state %s", c.Name, c.State)
					break
				}
			}
			if allRunning {
				writeJSON(w, http.StatusOK, map[string]any{
					"selector":  projectID,
					"converged": true,
				})
				return
			}
		} else if err != nil {
			lastReason = "docker list error: " + err.Error()
		} else {
			lastReason = "no containers found for project"
		}

		if time.Now().After(deadline) {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"selector":  projectID,
				"converged": false,
				"reason":    lastReason,
			})
			return
		}

		select {
		case <-ctx.Done():
			writeJSON(w, http.StatusAccepted, map[string]any{
				"selector":  projectID,
				"converged": false,
				"reason":    "request cancelled",
			})
			return
		case <-ticker.C:
			// poll again
		}
	}
}

// ─── PKI handlers ─────────────────────────────────────────────────────────────

// handlePKIIssueCert issues a new leaf (ClientAuth) certificate for a gateway.
// The private key is returned to the caller exactly once and is NOT stored server-side.
func (s *Server) handlePKIIssueCert(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	sid := chi.URLParam(r, "sid")

	var req struct {
		Gateway    string `json:"gateway"`    // gateway slug (≤63 chars)
		TTLDays    int    `json:"ttl_days"`   // 0 → 365
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Gateway == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("gateway name is required"))
		return
	}
	// Validate gateway slug.
	if err := validateSlugHTTP(w, req.Gateway); err != nil {
		return
	}
	if req.TTLDays <= 0 {
		req.TTLDays = 365
	}

	// Verify site exists and belongs to the tenant.
	if _, err := s.store.GetSite(r.Context(), sid); err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("site %q not found", sid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if len(s.cfg.MasterKey) == 0 {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("PKI unavailable: daemon started without master key (dev mode)"))
		return
	}

	caRow, err := s.store.GetCACert(r.Context(), sid)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no CA found for site %q; provision the site first", sid))
		return
	}

	ca := &pki.CA{
		CertPEM:      []byte(caRow.CertPEM),
		KeyEncrypted: caRow.KeyEnc,
		KeyNonce:     caRow.KeyNonce,
	}
	if err := ca.ParseCert(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("parse CA cert: %w", err))
		return
	}
	if err := ca.DecryptKey(s.cfg.MasterKey); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("decrypt CA key: %w", err))
		return
	}

	cn := req.Gateway
	if len(cn) > 63 {
		cn = cn[:63]
	}
	leaf, err := pki.IssueLeafCert(ca, cn, req.TTLDays)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("issue cert: %w", err))
		return
	}

	// Store the cert metadata (NOT the private key).
	certRow := sqlite.CertRow{
		SiteID:      sid,
		Kind:        "gateway",
		CommonName:  cn,
		CertPEM:     string(leaf.CertPEM),
		Fingerprint: leaf.Fingerprint,
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
	}
	if storeErr := s.store.StoreCert(r.Context(), certRow); storeErr != nil && storeErr != sqlite.ErrDuplicate {
		s.log.Warn("store gateway cert metadata failed", "site", sid, "fingerprint", leaf.Fingerprint, "err", storeErr)
	}

	_ = s.audit.Emit(r.Context(), audit.Event{
		Kind:      audit.KindPKIIssueCert,
		TenantID:  tid,
		SiteID:    sid,
		ProjectID: s.tenantProjectID(r.Context(), tid),
		Detail:    fmt.Sprintf(`{"cn":%q,"fingerprint":%q}`, cn, leaf.Fingerprint),
		Success:   true,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"fingerprint": leaf.Fingerprint,
		"cert_pem":    string(leaf.CertPEM),
		"key_pem":     string(leaf.KeyPEM), // returned exactly once, never persisted
		"not_before":  leaf.NotBefore.Format(time.RFC3339),
		"not_after":   leaf.NotAfter.Format(time.RFC3339),
	})
}

// handlePKIRevokeCert revokes a certificate by fingerprint and, for EMQX sites,
// queues a banned-list push. For mosquitto sites, marks revoked in SQLite
// (CRL-based enforcement is handled by a future container restart).
func (s *Server) handlePKIRevokeCert(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	sid := chi.URLParam(r, "sid")
	fp  := chi.URLParam(r, "fp")

	// Verify site exists.
	if _, err := s.store.GetSite(r.Context(), sid); err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("site %q not found", sid))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if err := s.store.RevokeCert(r.Context(), fp); err != nil {
		if err == sqlite.ErrNotFound {
			writeErr(w, http.StatusNotFound, fmt.Errorf("cert %q not found or already revoked", fp))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	_ = s.audit.Emit(r.Context(), audit.Event{
		Kind:      audit.KindPKIRevokeCert,
		TenantID:  tid,
		SiteID:    sid,
		ProjectID: s.tenantProjectID(r.Context(), tid),
		Detail:    fmt.Sprintf(`{"fingerprint":%q}`, fp),
		Success:   true,
	})

	// For mosquitto-backed sites, revocation is enforced by restarting the broker
	// so it re-reads the updated CRL / ACL. For EMQX, the EMQX banned-list
	// push is handled asynchronously by the reconciler on the next tick.
	siteRow, siteErr := s.store.GetSite(r.Context(), sid)
	if siteErr == nil && siteRow.BrokerKind == "mosquitto" {
		composeFile := fmt.Sprintf("%s/tenants/%s/sites/%s/docker-compose.yml", s.cfg.DataDir, tid, sid)
		projectID := tid + "-" + sid
		if restartErr := s.restartBroker(r.Context(), projectID, composeFile); restartErr != nil {
			s.log.Warn("mosquitto restart after revocation failed", "site", sid, "err", restartErr)
			_ = s.audit.Emit(r.Context(), audit.Event{
				Kind:     audit.KindBrokerRestart,
				TenantID: tid,
				SiteID:   sid,
				Detail:   fmt.Sprintf(`{"err":%q,"trigger":"revocation"}`, restartErr.Error()),
				Success:  false,
			})
		} else {
			_ = s.audit.Emit(r.Context(), audit.Event{
				Kind:     audit.KindBrokerRestart,
				TenantID: tid,
				SiteID:   sid,
				Detail:   `{"trigger":"revocation"}`,
				Success:  true,
			})
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// restartBroker issues a docker compose restart for the "broker" service in
// the given project. Used by certificate revocation for mosquitto sites.
func (s *Server) restartBroker(ctx context.Context, projectID, composeFile string) error {
	cmd := exec.CommandContext(ctx, "docker", "compose",
		"-p", projectID, "-f", composeFile, "restart", "broker")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart broker %s: %w (output: %s)", projectID, err, string(out))
	}
	return nil
}

// installBackupTimer installs the per-tenant systemd backup timer (best-effort).
// Errors are logged but never propagate to the caller.
func installBackupTimer(tenantID string, devMode bool, log *slog.Logger) {
	if devMode {
		return // skip in dev mode
	}
	timerContent := backup.SystemdTimerUnit(tenantID)
	timerPath := "/etc/systemd/system/controlai-backup-" + tenantID + ".timer"
	if err := os.WriteFile(timerPath, []byte(timerContent), 0o644); err != nil {
		// silently skip — systemd may not be present (containers, macOS, etc.)
		return
	}
	// Enable and start the timer (best-effort).
	if err := execSystemctl("enable", "--now", timerPath); err != nil {
		log.Warn("enable backup timer failed", "tenant", tenantID, "err", err)
	}
}

// execSystemctl runs a systemctl subcommand, returning any error.
func execSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %v: %w (output: %s)", args, err, string(out))
	}
	return nil
}

// ─── Token auth middleware (TCP transport only) ───────────────────────────────

func (s *Server) tokenAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, fmt.Errorf("missing bearer token"))
			return
		}
		token := strings.TrimPrefix(hdr, "Bearer ")
		if _, err := s.store.LookupToken(r.Context(), token); err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, fmt.Errorf("invalid or revoked token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Admission ────────────────────────────────────────────────────────────────

// admissionCheck runs capacity prediction and returns an error if the plan would
// exceed 85% of usable RAM. newSite may be nil (tenant-only addition).
func (s *Server) admissionCheck(ctx context.Context, newTenantID string, newSite *sqlite.SiteRow) error {
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		return err
	}
	allSites, err := s.store.ListAllSites(ctx)
	if err != nil {
		return err
	}
	// Build the plan including the proposed addition.
	tenantMap := map[string]bool{}
	for _, t := range tenants {
		tenantMap[t.ID] = true
	}
	var plan []capacity.TenantPlan
	seenTenants := map[string]*capacity.TenantPlan{}
	for _, t := range tenants {
		tp := &capacity.TenantPlan{TenantID: t.ID}
		seenTenants[t.ID] = tp
		plan = append(plan, *tp)
	}
	for _, site := range allSites {
		tp, ok := seenTenants[site.TenantID]
		if !ok {
			continue
		}
		tp.Sites = append(tp.Sites, capacity.SitePlan{
			SiteID:     site.ID,
			BrokerKind: site.BrokerKind,
			Tier:       site.Throughput,
			Direction:  site.Direction,
		})
	}
	// Add the new tenant if it doesn't already exist.
	if !tenantMap[newTenantID] {
		newTP := capacity.TenantPlan{TenantID: newTenantID}
		if newSite != nil {
			newTP.Sites = []capacity.SitePlan{{
				SiteID:     "proposed",
				BrokerKind: newSite.BrokerKind,
				Tier:       newSite.Throughput,
				Direction:  newSite.Direction,
			}}
		}
		plan = append(plan, newTP)
	} else if newSite != nil {
		// Add new site to existing tenant.
		for i := range plan {
			if plan[i].TenantID == newTenantID {
				plan[i].Sites = append(plan[i].Sites, capacity.SitePlan{
					SiteID:     "proposed",
					BrokerKind: newSite.BrokerKind,
					Tier:       newSite.Throughput,
					Direction:  newSite.Direction,
				})
				break
			}
		}
	}
	memKB, err := capacity.ReadMemTotalKB()
	if err != nil {
		return err
	}
	pred, err := capacity.Predict(plan, memKB)
	if err != nil {
		return err
	}
	if !pred.Admissible {
		return fmt.Errorf("capacity limit exceeded: projected %d MB > allowed %d MB (%d MB headroom remaining; add fewer tenants or sites)",
			pred.ProjectedMB, pred.MaxAllowedMB, pred.HeadroomMB)
	}
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// tenantProjectID returns the project_id for a tenant, or empty string on any error.
// Used for best-effort audit log enrichment — failures are non-fatal.
func (s *Server) tenantProjectID(ctx context.Context, tenantID string) string {
	if tenantID == "" {
		return ""
	}
	t, err := s.store.GetTenant(ctx, tenantID)
	if err != nil {
		return ""
	}
	return t.ProjectID
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// validateProjectIDHTTP validates the project_id field: 1–64 alphanumeric, underscore,
// or hyphen characters. Empty string is allowed (means "clear") — callers decide
// whether empty is acceptable before calling this.
func validateProjectIDHTTP(w http.ResponseWriter, projectID string) error {
	if len(projectID) > 64 {
		err := fmt.Errorf("project_id %q exceeds 64 characters (max 64 characters of [a-zA-Z0-9_-])", projectID)
		writeErr(w, http.StatusBadRequest, err)
		return err
	}
	for _, c := range projectID {
		valid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-'
		if !valid {
			err := fmt.Errorf("project_id %q contains invalid character %q: must match [a-zA-Z0-9_-]{1,64}", projectID, c)
			writeErr(w, http.StatusBadRequest, err)
			return err
		}
	}
	return nil
}

func validateSlugHTTP(w http.ResponseWriter, slug string) error {
	if slug == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("slug is required"))
		return fmt.Errorf("slug is required")
	}
	// Validate pattern ^[a-z][a-z0-9-]{0,40}$
	if len(slug) < 1 || len(slug) > 41 {
		err := fmt.Errorf("slug %q must be 1-41 chars matching ^[a-z][a-z0-9-]{0,40}$ (lowercase, digits, hyphens, start with letter)", slug)
		writeErr(w, http.StatusBadRequest, err)
		return err
	}
	for i, c := range slug {
		valid := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9' && i > 0) || (c == '-' && i > 0)
		if !valid {
			err := fmt.Errorf("slug %q must match ^[a-z][a-z0-9-]{0,40}$ (lowercase letters, digits, hyphens; must start with a letter)", slug)
			writeErr(w, http.StatusBadRequest, err)
			return err
		}
	}
	return nil
}


