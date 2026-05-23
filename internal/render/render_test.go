package render_test

import (
	"path/filepath"
	"strings"
	"testing"

	"controlai/internal/render"
)

// baseCtx returns a minimal render context for testing.
func baseTenantCtx() render.TenantCtx {
	return render.TenantCtx{
		ID:                "tnt_acme-corp",
		Name:              "Acme Corp",
		Domain:            "acme.example.com",
		Retention:         "7d",
		ChunkTimeInterval: "1 hour",
		CompressionOn:     true,
		SchemaVersion:     1,
	}
}

func baseSiteCtx(brokerKind string) *render.SiteCtx {
	replicas := 1
	shared := false
	if brokerKind == "emqx" {
		replicas = 1
	}
	return &render.SiteCtx{
		ID:                    "ste_seoul",
		TenantID:              "tnt_acme-corp",
		BrokerKind:            brokerKind,
		Throughput:            "low",
		Direction:             "uni",
		PayloadCodec:          "cbor",
		IngestReplicas:        replicas,
		BatchSize:             200,
		FlushIntervalMS:       1000,
		UseSharedSubscription: shared,
		MQTTTopicFilter:       "tnt_acme-corp/ste_seoul/+/+",
		SNIHostname:           "ste_seoul.tnt_acme-corp.acme.example.com",
		SANs:                  []string{"ste_seoul.tnt_acme-corp.acme.example.com"},
		SchemaVersion:         1,
	}
}

func baseCredsCtx() *render.CredsCtx {
	return &render.CredsCtx{
		SuperuserName: "controlai_admin",
		SuperuserPass: "test_super_pass",
		IngestName:    "controlai_ingest",
		IngestPass:    "test_ingest_pass",
		EMQXKeyID:     "emqx_key_id",
		EMQXKeySecret: "emqx_key_secret",
	}
}

func TestRenderShared(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Shared: &render.SharedCtx{ACMEEnabled: false, Domain: "acme.example.com"},
	}
	results, err := r.RenderShared(ctx)
	if err != nil {
		t.Fatalf("RenderShared: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result from RenderShared")
	}
	// Expect docker-compose.yml for Traefik
	found := false
	for _, rr := range results {
		if rr.RelPath == "docker-compose.yml" {
			found = true
			if !strings.Contains(string(rr.Content), "traefik") {
				t.Error("expected traefik in shared docker-compose.yml")
			}
		}
	}
	if !found {
		t.Error("expected docker-compose.yml in RenderShared output")
	}
	// Every result must have a non-empty hash.
	for _, rr := range results {
		if rr.Hash == "" {
			t.Errorf("result %s has empty hash", rr.RelPath)
		}
	}
}

func TestRenderTenantTSDB(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Creds:  baseCredsCtx(),
	}
	results, err := r.RenderTenantTSDB(ctx)
	if err != nil {
		t.Fatalf("RenderTenantTSDB: %v", err)
	}
	// Expect docker-compose.yml, init.sql, postgresql.conf
	relpaths := map[string]bool{}
	for _, rr := range results {
		relpaths[rr.RelPath] = true
	}
	for _, expected := range []string{"docker-compose.yml", "init.sql", "postgresql.conf"} {
		if !relpaths[expected] {
			t.Errorf("expected %s in TSDB render output, got %v", expected, relpaths)
		}
	}
}

func TestRenderTenantTSDB_CompressionOn(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: render.TenantCtx{
			ID: "tnt_test", Domain: "test.com", Retention: "30d",
			ChunkTimeInterval: "6 hours", CompressionOn: true, SchemaVersion: 1,
		},
		Creds: baseCredsCtx(),
	}
	results, err := r.RenderTenantTSDB(ctx)
	if err != nil {
		t.Fatalf("RenderTenantTSDB: %v", err)
	}
	for _, rr := range results {
		if rr.RelPath == "init.sql" {
			if !strings.Contains(string(rr.Content), "add_compression_policy") {
				t.Error("expected add_compression_policy in init.sql for 30d retention")
			}
			return
		}
	}
	t.Error("init.sql not found in output")
}

func TestRenderTenantTSDB_CompressionOff(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: render.TenantCtx{
			ID: "tnt_test", Domain: "test.com", Retention: "1h",
			ChunkTimeInterval: "1 minute", CompressionOn: false, SchemaVersion: 1,
		},
		Creds: baseCredsCtx(),
	}
	results, err := r.RenderTenantTSDB(ctx)
	if err != nil {
		t.Fatalf("RenderTenantTSDB: %v", err)
	}
	for _, rr := range results {
		if rr.RelPath == "init.sql" {
			if strings.Contains(string(rr.Content), "add_compression_policy") {
				t.Error("expected NO add_compression_policy in init.sql for 1h retention")
			}
			return
		}
	}
	t.Error("init.sql not found in output")
}

func TestRenderSite_Mosquitto_ProducesSingleCompose(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Site:   baseSiteCtx("mosquitto"),
		Creds:  baseCredsCtx(),
	}
	results, err := r.RenderSite(ctx)
	if err != nil {
		t.Fatalf("RenderSite mosquitto: %v", err)
	}

	composeCount := 0
	for _, rr := range results {
		if filepath.Base(rr.RelPath) == "docker-compose.yml" {
			composeCount++
		}
	}
	if composeCount != 1 {
		t.Errorf("expected exactly 1 docker-compose.yml, got %d (paths: %v)",
			composeCount, relPaths(results))
	}
}

func TestRenderSite_Mosquitto_HasBrokerAndIngest(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Site:   baseSiteCtx("mosquitto"),
		Creds:  baseCredsCtx(),
	}
	results, err := r.RenderSite(ctx)
	if err != nil {
		t.Fatalf("RenderSite: %v", err)
	}
	for _, rr := range results {
		if rr.RelPath == "docker-compose.yml" {
			content := string(rr.Content)
			if !strings.Contains(content, "broker:") {
				t.Error("docker-compose.yml missing broker service")
			}
			if !strings.Contains(content, "ingest-0:") {
				t.Error("docker-compose.yml missing ingest-0 service")
			}
			return
		}
	}
	t.Error("docker-compose.yml not found in output")
}

func TestRenderSite_Mosquitto_ConfigFilesUnderDeploy(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Site:   baseSiteCtx("mosquitto"),
		Creds:  baseCredsCtx(),
	}
	results, err := r.RenderSite(ctx)
	if err != nil {
		t.Fatalf("RenderSite: %v", err)
	}
	paths := relPaths(results)
	for _, expected := range []string{"deploy/mosquitto.conf", "deploy/acl.conf"} {
		found := false
		for _, p := range paths {
			if p == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in render output, got %v", expected, paths)
		}
	}
	// Old root-level configs must NOT appear (they were replaced with empty stubs).
	for _, forbidden := range []string{"mosquitto.conf", "acl.conf"} {
		for _, p := range paths {
			if p == forbidden {
				t.Errorf("root-level %q should not appear (must be under deploy/)", forbidden)
			}
		}
	}
}

func TestRenderSite_EMQX_ProducesSingleCompose(t *testing.T) {
	r := render.New()
	site := baseSiteCtx("emqx")
	site.IngestReplicas = 2
	site.UseSharedSubscription = true
	site.MQTTTopicFilter = "$share/ste_seoul/tnt_acme-corp/ste_seoul/+/+"
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Site:   site,
		Creds:  baseCredsCtx(),
	}
	results, err := r.RenderSite(ctx)
	if err != nil {
		t.Fatalf("RenderSite emqx: %v", err)
	}
	composeCount := 0
	for _, rr := range results {
		if filepath.Base(rr.RelPath) == "docker-compose.yml" {
			composeCount++
		}
	}
	if composeCount != 1 {
		t.Errorf("expected exactly 1 docker-compose.yml, got %d (paths: %v)",
			composeCount, relPaths(results))
	}
}

func TestRenderSite_EMQX_HasTwoIngestReplicas(t *testing.T) {
	r := render.New()
	site := baseSiteCtx("emqx")
	site.IngestReplicas = 2
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Site:   site,
		Creds:  baseCredsCtx(),
	}
	results, err := r.RenderSite(ctx)
	if err != nil {
		t.Fatalf("RenderSite: %v", err)
	}
	for _, rr := range results {
		if rr.RelPath == "docker-compose.yml" {
			content := string(rr.Content)
			if !strings.Contains(content, "ingest-0:") {
				t.Error("missing ingest-0 in EMQX compose")
			}
			if !strings.Contains(content, "ingest-1:") {
				t.Error("missing ingest-1 in EMQX compose for 2-replica plan")
			}
			return
		}
	}
	t.Error("docker-compose.yml not found")
}

func TestRenderTraefikDynamicForSite_PerSiteNaming(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Site:   baseSiteCtx("mosquitto"),
	}
	results, err := r.RenderTraefikDynamicForSite(ctx)
	if err != nil {
		t.Fatalf("RenderTraefikDynamicForSite: %v", err)
	}
	for _, rr := range results {
		if !strings.HasPrefix(rr.RelPath, "tnt_acme-corp-ste_seoul-") {
			t.Errorf("expected per-site prefix in dynamic config filename, got %q", rr.RelPath)
		}
		if !strings.Contains(string(rr.Content), "ste_seoul.tnt_acme-corp.acme.example.com") {
			t.Errorf("expected SNI hostname in dynamic config, content: %s", rr.Content)
		}
	}
}

func TestRenderIdempotent(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Site:   baseSiteCtx("mosquitto"),
		Creds:  baseCredsCtx(),
	}
	results1, err := r.RenderSite(ctx)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	results2, err := r.RenderSite(ctx)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if len(results1) != len(results2) {
		t.Fatalf("result count differs: %d vs %d", len(results1), len(results2))
	}
	for i := range results1 {
		if results1[i].Hash != results2[i].Hash {
			t.Errorf("file %s hash differs between renders: %s vs %s",
				results1[i].RelPath, results1[i].Hash, results2[i].Hash)
		}
	}
}

func TestRenderSite_IngestTSDBCredsInjected(t *testing.T) {
	r := render.New()
	ctx := render.RenderContext{
		Tenant: baseTenantCtx(),
		Site:   baseSiteCtx("mosquitto"),
		Creds:  baseCredsCtx(),
	}
	results, err := r.RenderSite(ctx)
	if err != nil {
		t.Fatalf("RenderSite: %v", err)
	}
	for _, rr := range results {
		if rr.RelPath == "docker-compose.yml" {
			content := string(rr.Content)
			if !strings.Contains(content, "test_ingest_pass") {
				t.Error("expected ingest TSDB password injected into docker-compose.yml")
			}
			if !strings.Contains(content, "controlai_ingest") {
				t.Error("expected ingest TSDB username injected into docker-compose.yml")
			}
			return
		}
	}
	t.Error("docker-compose.yml not found")
}

func relPaths(results []render.RenderResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.RelPath
	}
	return out
}
