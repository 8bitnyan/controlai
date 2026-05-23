// Package daemon — provision.go contains helpers for rendering templates,
// generating credentials, and writing artifacts to disk during tenant/site
// create and update flows. These helpers ensure the reconciler always finds
// a valid compose file on disk before it is asked to converge a project.
package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"controlai/internal/config"
	"controlai/internal/render"
	"controlai/internal/store/sqlite"
)

// randomHex returns n random bytes encoded as a hex string (2n chars).
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ensureTSDBCreds returns existing TSDB credentials for tenantID, or generates
// and stores new ones if none exist yet.
func ensureTSDBCreds(ctx context.Context, tenantID string, db *sqlite.Store) (*sqlite.TSDBCreds, error) {
	creds, err := db.GetTSDBCreds(ctx, tenantID)
	if err == nil {
		return creds, nil
	}
	c := sqlite.TSDBCreds{
		TenantID:      tenantID,
		SuperuserName: "controlai_admin",
		SuperuserPass: randomHex(16),
		IngestName:    "controlai_ingest",
		IngestPass:    randomHex(16),
	}
	if err := db.UpsertTSDBCreds(ctx, c); err != nil {
		return nil, fmt.Errorf("store TSDB creds for %s: %w", tenantID, err)
	}
	return &c, nil
}

// ensureEMQXKey returns the existing EMQX REST API key for siteID, or
// generates and stores a new one if none exists.
func ensureEMQXKey(ctx context.Context, siteID string, db *sqlite.Store) (*sqlite.EMQXAPIKey, error) {
	key, err := db.GetEMQXKey(ctx, siteID)
	if err == nil {
		return key, nil
	}
	k := &sqlite.EMQXAPIKey{
		SiteID:    siteID,
		KeyID:     "controlai-" + siteID,
		KeySecret: randomHex(20),
	}
	if err := db.UpsertEMQXKey(ctx, *k); err != nil {
		return nil, fmt.Errorf("store EMQX key for %s: %w", siteID, err)
	}
	return k, nil
}

// buildTenantRenderCtx assembles a RenderContext for the per-tenant TSDB project.
func buildTenantRenderCtx(ctx context.Context, tenantID string, db *sqlite.Store) (render.RenderContext, error) {
	tenantRow, err := db.GetTenant(ctx, tenantID)
	if err != nil {
		return render.RenderContext{}, fmt.Errorf("get tenant %s: %w", tenantID, err)
	}
	creds, err := ensureTSDBCreds(ctx, tenantID, db)
	if err != nil {
		return render.RenderContext{}, err
	}
	tenantCfg := &config.Tenant{Retention: config.Retention(tenantRow.Retention)}
	return render.RenderContext{
		Tenant: render.TenantCtx{
			ID:                tenantRow.ID,
			Name:              tenantRow.Name,
			Domain:            tenantRow.Domain,
			Retention:         tenantRow.Retention,
			ChunkTimeInterval: tenantCfg.ChunkTimeInterval(),
			CompressionOn:     tenantCfg.CompressionEnabled(),
			SchemaVersion:     tenantRow.SchemaVersion,
		},
		Creds: &render.CredsCtx{
			SuperuserName: creds.SuperuserName,
			SuperuserPass: creds.SuperuserPass,
			IngestName:    creds.IngestName,
			IngestPass:    creds.IngestPass,
		},
	}, nil
}

// buildSiteRenderCtx assembles a RenderContext for a site broker+ingest project.
func buildSiteRenderCtx(ctx context.Context, siteID string, db *sqlite.Store, dataDir string) (render.RenderContext, error) {
	siteRow, err := db.GetSite(ctx, siteID)
	if err != nil {
		return render.RenderContext{}, fmt.Errorf("get site %s: %w", siteID, err)
	}
	tenantRow, err := db.GetTenant(ctx, siteRow.TenantID)
	if err != nil {
		return render.RenderContext{}, fmt.Errorf("get tenant %s: %w", siteRow.TenantID, err)
	}
	creds, err := ensureTSDBCreds(ctx, siteRow.TenantID, db)
	if err != nil {
		return render.RenderContext{}, err
	}

	siteCfg := &config.Site{
		ID:           siteRow.ID,
		TenantID:     siteRow.TenantID,
		Broker:       config.BrokerConfig{Kind: config.BrokerKind(siteRow.BrokerKind)},
		Ingest:       config.IngestConfig{Direction: config.IngestDirection(siteRow.Direction)},
		Throughput:   config.ThroughputTier(siteRow.Throughput),
		PayloadCodec: config.PayloadCodec(siteRow.PayloadCodec),
	}

	sniHostname := siteRow.ID + "." + siteRow.TenantID + "." + tenantRow.Domain
	certDir := filepath.Join(dataDir, "tenants", siteRow.TenantID, "sites", siteRow.ID, "deploy", "certs", "active")

	rctx := render.RenderContext{
		Tenant: render.TenantCtx{
			ID:     tenantRow.ID,
			Name:   tenantRow.Name,
			Domain: tenantRow.Domain,
		},
		Site: &render.SiteCtx{
			ID:                    siteRow.ID,
			TenantID:              siteRow.TenantID,
			BrokerKind:            siteRow.BrokerKind,
			Throughput:            siteRow.Throughput,
			Direction:             siteRow.Direction,
			PayloadCodec:          siteRow.PayloadCodec,
			IngestReplicas:        siteCfg.IngestReplicas(),
			BatchSize:             siteCfg.BatchSize(),
			FlushIntervalMS:       siteCfg.FlushIntervalMS(),
			UseSharedSubscription: siteCfg.UseSharedSubscription(),
			MQTTTopicFilter:       siteCfg.MQTTTopicFilter(),
			SNIHostname:           sniHostname,
			SANs:                  []string{sniHostname},
			SchemaVersion:         siteRow.SchemaVersion,
		},
		Creds: &render.CredsCtx{
			SuperuserName: creds.SuperuserName,
			SuperuserPass: creds.SuperuserPass,
			IngestName:    creds.IngestName,
			IngestPass:    creds.IngestPass,
		},
		PKI: &render.PKICtx{
			CAPath:   filepath.Join(certDir, "ca.crt"),
			CertPath: filepath.Join(certDir, "server.crt"),
			KeyPath:  filepath.Join(certDir, "server.key"),
		},
	}

	// For EMQX sites, inject the REST API key.
	if siteRow.BrokerKind == "emqx" {
		key, err := ensureEMQXKey(ctx, siteRow.ID, db)
		if err != nil {
			return render.RenderContext{}, err
		}
		rctx.Creds.EMQXKeyID = key.KeyID
		rctx.Creds.EMQXKeySecret = key.KeySecret
	}

	return rctx, nil
}

// computeCombinedHash returns a single SHA-256 hex digest covering all rendered
// files (sorted by RelPath for determinism). This is stored in desired_state as
// the "expected" hash that the reconciler uses to detect config drift.
func computeCombinedHash(results []render.RenderResult) string {
	// Sort by path for deterministic ordering.
	sorted := make([]render.RenderResult, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].RelPath < sorted[j].RelPath
	})
	h := sha256.New()
	for _, r := range sorted {
		_, _ = fmt.Fprintf(h, "%s:%s\n", r.RelPath, r.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// renderAndWriteTSDB renders the per-tenant TSDB compose project, writes
// artifacts to disk, stores a combined hash in actual_state_cache, and
// upserts the desired_state row so the reconciler will converge the project.
//
// projectID follows the convention: "{tenantID}-tsdb".
func renderAndWriteTSDB(ctx context.Context, tenantID string, db *sqlite.Store,
	renderer *render.Renderer, dataDir string) (combinedHash string, err error) {

	rctx, err := buildTenantRenderCtx(ctx, tenantID, db)
	if err != nil {
		return "", err
	}
	results, err := renderer.RenderTenantTSDB(rctx)
	if err != nil {
		return "", fmt.Errorf("render TSDB for %s: %w", tenantID, err)
	}
	outDir := filepath.Join(dataDir, "tenants", tenantID, "tsdb")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	if err := render.WriteResults(outDir, results); err != nil {
		return "", fmt.Errorf("write TSDB files for %s: %w", tenantID, err)
	}
	combined := computeCombinedHash(results)
	projectID := tenantID + "-tsdb"
	// Store desired hash. The actual hash is updated by the reconciler only
	// after a successful compose up, so the reconciler can detect drift when
	// desired ≠ actual.
	_ = db.UpsertDesiredState(ctx, sqlite.DesiredStateRow{
		ProjectID:  projectID,
		TenantID:   tenantID,
		State:      "running",
		ConfigHash: combined,
	})
	return combined, nil
}

// renderAndWriteSite renders a site's broker+ingest compose project, writes
// artifacts to disk, writes the Traefik dynamic config, stores a combined hash,
// and upserts the desired_state row.
//
// projectID follows the convention: "{tenantID}-{siteID}".
func renderAndWriteSite(ctx context.Context, siteID string, db *sqlite.Store,
	renderer *render.Renderer, dataDir string) (combinedHash string, err error) {

	siteRow, err := db.GetSite(ctx, siteID)
	if err != nil {
		return "", fmt.Errorf("get site %s: %w", siteID, err)
	}
	rctx, err := buildSiteRenderCtx(ctx, siteID, db, dataDir)
	if err != nil {
		return "", err
	}

	// Render site broker+ingest templates.
	results, err := renderer.RenderSite(rctx)
	if err != nil {
		return "", fmt.Errorf("render site %s: %w", siteID, err)
	}
	outDir := filepath.Join(dataDir, "tenants", siteRow.TenantID, "sites", siteID)
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	if err := render.WriteResults(outDir, results); err != nil {
		return "", fmt.Errorf("write site files for %s: %w", siteID, err)
	}

	// Render and write Traefik dynamic config (atomic rename, for fsnotify).
	dynamicDir := filepath.Join(dataDir, "shared", "traefik", "dynamic")
	if err := os.MkdirAll(dynamicDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir traefik dynamic: %w", err)
	}
	dynResults, err := renderer.RenderTraefikDynamicForSite(rctx)
	if err != nil {
		return "", fmt.Errorf("render traefik dynamic for %s: %w", siteID, err)
	}
	if err := render.WriteResults(dynamicDir, dynResults); err != nil {
		return "", fmt.Errorf("write traefik dynamic for %s: %w", siteID, err)
	}

	// Compute combined hash of site files only (not Traefik dynamic, which is
	// shared infrastructure and doesn't drive site container restarts).
	// The desired ConfigHash is stored here; the actual hash is written by the
	// reconciler only after a successful compose up, enabling drift detection.
	combined := computeCombinedHash(results)
	projectID := siteRow.TenantID + "-" + siteID
	_ = db.UpsertDesiredState(ctx, sqlite.DesiredStateRow{
		ProjectID:  projectID,
		TenantID:   siteRow.TenantID,
		SiteID:     siteID,
		State:      "running",
		ConfigHash: combined,
	})
	return combined, nil
}
