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
	"controlai/internal/capacity"
	"controlai/internal/recon"
	"controlai/internal/store/sqlite"
	"controlai/internal/version"
)

// Config holds daemon server configuration.
type Config struct {
	// SocketPath is the unix socket path (default /var/run/controlai.sock).
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
}

// Server is the REST API server.
type Server struct {
	cfg    Config
	store  *sqlite.Store
	audit  audit.Emitter
	recon  *recon.Reconciler
	router chi.Router
	log    *slog.Logger
}

// New constructs a new Server.
func New(cfg Config, store *sqlite.Store, ae audit.Emitter, rec *recon.Reconciler, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: store, audit: ae, recon: rec, log: log}
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

	// Bi-mode downlink
	r.Post("/v1/tenants/{tid}/sites/{sid}/publish", s.handlePublish)

	// Logs
	r.Get("/v1/tenants/{tid}/sites/{sid}/logs", s.handleLogs)

	// Apply
	r.Post("/v1/apply/{selector}", s.handleApply)

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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if err := validateSlugHTTP(w, req.Slug); err != nil {
		return
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
	})
	if err != nil {
		if err == sqlite.ErrDuplicate {
			writeErr(w, http.StatusConflict, fmt.Errorf("tenant %q already exists", tenantID))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindTenantCreate, TenantID: tenantID, Success: true})
	writeJSON(w, http.StatusCreated, map[string]string{"id": tenantID})
}

func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListTenants(r.Context())
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
	// PATCH placeholder — returns 501 until retention-change reconciler is wired
	writeErr(w, http.StatusNotImplemented, fmt.Errorf("PATCH /v1/tenants/{id} not yet implemented"))
}

func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "tid")
	purge := r.URL.Query().Get("purge") == "true"
	if err := s.store.DeleteTenant(r.Context(), tid, purge); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindTenantDelete, TenantID: tid,
		Detail: fmt.Sprintf(`{"purge":%t}`, purge), Success: true})
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
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindSiteCreate, TenantID: tid, SiteID: siteID, Success: true})
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
	writeErr(w, http.StatusNotImplemented, fmt.Errorf("PATCH /v1/tenants/{tid}/sites/{sid} not yet implemented"))
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	tid := chi.URLParam(r, "tid")
	purge := r.URL.Query().Get("purge") == "true"
	if err := s.store.DeleteSite(r.Context(), sid, purge); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.audit.Emit(r.Context(), audit.Event{Kind: audit.KindSiteDelete, TenantID: tid, SiteID: sid,
		Detail: fmt.Sprintf(`{"purge":%t}`, purge), Success: true})
	w.WriteHeader(http.StatusNoContent)
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
	writeJSON(w, http.StatusAccepted, map[string]string{"selector": selector, "status": "applying"})
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
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


