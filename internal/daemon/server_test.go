package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"controlai/internal/daemon"
	"controlai/internal/store/sqlite"
)

// openTestStore opens a temporary SQLite store, cleaning up on test exit.
func openTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newTestServer builds a daemon.Server and an httptest.Server wrapping it.
func newTestServer(t *testing.T, store *sqlite.Store) *httptest.Server {
	t.Helper()
	lastTick := time.Now()
	srv := daemon.New(daemon.Config{
		SocketPath:  "/tmp/controlai-test.sock",
		DataDir:     t.TempDir(),
		DevMode:     true,
		StartedAt:   time.Now(),
		ReconcilerLastTick: &lastTick,
		DockerReachable: func(ctx context.Context) bool { return false },
	}, store, store, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	return httptest.NewServer(srv.Handler())
}

// postJSON issues a POST with a JSON body.
func postJSON(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := ts.Client().Post(ts.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func getJSON(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func deleteReq(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

func patchJSON(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	return resp
}

// ─── Health ────────────────────────────────────────────────────────────────

func TestHandleHealth(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	resp := getJSON(t, ts, "/v1/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	resp.Body.Close()

	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body["status"])
	}
	if body["version"] == nil {
		t.Error("version missing from health response")
	}
	if _, ok := body["docker_reachable"]; !ok {
		t.Error("docker_reachable missing from health response")
	}
	if _, ok := body["registry_healthy"]; !ok {
		t.Error("registry_healthy missing from health response")
	}
	if _, ok := body["reconciler_last_tick"]; !ok {
		t.Error("reconciler_last_tick missing from health response")
	}
}

// ─── Capacity ─────────────────────────────────────────────────────────────

func TestHandleCapacity_Empty(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	resp := getJSON(t, ts, "/v1/capacity")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()

	if _, ok := body["admissible"]; !ok {
		t.Error("admissible missing from capacity response")
	}
	if _, ok := body["projected_mb"]; !ok {
		// Accept both snake_case JSON key variants.
		if _, ok2 := body["ProjectedMB"]; !ok2 {
			t.Error("projected capacity field missing from response")
		}
	}
}

// ─── Tenants ──────────────────────────────────────────────────────────────

func TestHandleCreateTenant_Success(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	resp := postJSON(t, ts, "/v1/tenants", map[string]string{
		"slug": "acme-corp", "retention": "7d",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["id"] != "tnt_acme-corp" {
		t.Errorf("expected id=tnt_acme-corp, got %q", body["id"])
	}
}

func TestHandleCreateTenant_DuplicateSlug(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "acme-corp"}).Body.Close()
	resp := postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "acme-corp"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate, got %d", resp.StatusCode)
	}
}

func TestHandleCreateTenant_InvalidSlug(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	cases := []string{"", "1invalid", "UPPER", "has space", "-leading-dash"}
	for _, slug := range cases {
		resp := postJSON(t, ts, "/v1/tenants", map[string]string{"slug": slug})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("slug %q: expected 400, got %d", slug, resp.StatusCode)
		}
	}
}

func TestHandleListTenants(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "t1"}).Body.Close()
	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "t2"}).Body.Close()

	resp := getJSON(t, ts, "/v1/tenants")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var rows []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) < 2 {
		t.Errorf("expected at least 2 tenants, got %d", len(rows))
	}
}

func TestHandleGetTenant_NotFound(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	resp := getJSON(t, ts, "/v1/tenants/tnt_no-such")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleDeleteTenant(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "del-me"}).Body.Close()

	resp := deleteReq(t, ts, "/v1/tenants/tnt_del-me")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Tenant should now be orphaned (soft delete), so GET should still find it
	// (or 404 depending on implementation) — just confirm no server error.
	resp2 := getJSON(t, ts, "/v1/tenants/tnt_del-me")
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusInternalServerError {
		t.Error("expected non-500 after delete")
	}
}

// ─── Sites ────────────────────────────────────────────────────────────────

func TestHandleCreateSite_Success(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "acme"}).Body.Close()

	resp := postJSON(t, ts, "/v1/tenants/tnt_acme/sites", map[string]string{
		"slug": "seoul", "broker_kind": "mosquitto", "throughput": "low",
		"direction": "uni", "payload_codec": "cbor",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["id"] != "ste_seoul" {
		t.Errorf("expected id=ste_seoul, got %q", body["id"])
	}
}

func TestHandleCreateSite_TenantNotFound(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	resp := postJSON(t, ts, "/v1/tenants/tnt_ghost/sites", map[string]string{
		"slug": "s1", "broker_kind": "mosquitto", "throughput": "low",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when tenant missing, got %d", resp.StatusCode)
	}
}

func TestHandleCreateSite_MosquittoMidRejected(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "acme"}).Body.Close()

	resp := postJSON(t, ts, "/v1/tenants/tnt_acme/sites", map[string]string{
		"slug": "bad", "broker_kind": "mosquitto", "throughput": "mid",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for mosquitto+mid, got %d", resp.StatusCode)
	}
}

func TestHandleCreateSite_HighTierRejected(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "acme"}).Body.Close()

	resp := postJSON(t, ts, "/v1/tenants/tnt_acme/sites", map[string]string{
		"slug": "bad-tier", "broker_kind": "emqx", "throughput": "high",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for high tier, got %d", resp.StatusCode)
	}
}

func TestHandleListSites(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "t1"}).Body.Close()
	postJSON(t, ts, "/v1/tenants/tnt_t1/sites", map[string]string{
		"slug": "s1", "broker_kind": "mosquitto", "throughput": "low",
	}).Body.Close()

	resp := getJSON(t, ts, "/v1/tenants/tnt_t1/sites")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var rows []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) < 1 {
		t.Error("expected at least 1 site")
	}
}

func TestHandleGetSite_NotFound(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "t1"}).Body.Close()

	resp := getJSON(t, ts, "/v1/tenants/tnt_t1/sites/ste_ghost")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleDeleteSite(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "t1"}).Body.Close()
	postJSON(t, ts, "/v1/tenants/tnt_t1/sites", map[string]string{
		"slug": "del-site", "broker_kind": "mosquitto", "throughput": "low",
	}).Body.Close()

	resp := deleteReq(t, ts, "/v1/tenants/tnt_t1/sites/ste_del-site")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

// ─── Publish (bi-mode downlink) ────────────────────────────────────────────

func TestHandlePublish_UniModeRejected(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "t1"}).Body.Close()
	postJSON(t, ts, "/v1/tenants/tnt_t1/sites", map[string]string{
		"slug": "s1", "broker_kind": "mosquitto", "throughput": "low", "direction": "uni",
	}).Body.Close()

	resp := postJSON(t, ts, "/v1/tenants/tnt_t1/sites/ste_s1/publish", map[string]any{
		"topic": "tnt_t1/ste_s1/device-1/cmd", "payload": "reboot", "qos": 1,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for uni-mode publish, got %d", resp.StatusCode)
	}
}

func TestHandlePublish_SiteNotFound(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	resp := postJSON(t, ts, "/v1/tenants/tnt_ghost/sites/ste_ghost/publish", map[string]any{
		"topic": "t/s/d/m", "payload": "x",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// ─── Apply ────────────────────────────────────────────────────────────────

func TestHandleApply(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	resp := postJSON(t, ts, "/v1/apply/some-project", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["selector"] != "some-project" {
		t.Errorf("expected selector=some-project, got %q", body["selector"])
	}
}

// ─── Token auth on TCP transport ──────────────────────────────────────────

func TestTokenAuthMiddleware_MissingToken(t *testing.T) {
	store := openTestStore(t)
	lastTick := time.Now()
	srv := daemon.New(daemon.Config{
		SocketPath:         "/tmp/controlai-test-tok.sock",
		DataDir:            t.TempDir(),
		DevMode:            true,
		StartedAt:          time.Now(),
		ReconcilerLastTick: &lastTick,
	}, store, store, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ts := httptest.NewServer(srv.TCPHandler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/v1/health")
	if err != nil {
		t.Fatalf("GET /v1/health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header on 401")
	}
}

func TestTokenAuthMiddleware_ValidToken(t *testing.T) {
	store := openTestStore(t)
	lastTick := time.Now()
	srv := daemon.New(daemon.Config{
		SocketPath:         "/tmp/controlai-test-tok2.sock",
		DataDir:            t.TempDir(),
		DevMode:            true,
		StartedAt:          time.Now(),
		ReconcilerLastTick: &lastTick,
	}, store, store, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ts := httptest.NewServer(srv.TCPHandler())
	defer ts.Close()

	rawToken, _, err := store.CreateToken(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/health with token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with valid token, got %d", resp.StatusCode)
	}
}

// ─── project_id handler tests ─────────────────────────────────────────────────

// TestHandleCreateTenant_WithProjectID verifies that POST /v1/tenants with a
// project_id persists the tag and includes it in the response (spec scenario
// "Create a tenant with project_id").
func TestHandleCreateTenant_WithProjectID(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	resp := postJSON(t, ts, "/v1/tenants", map[string]string{
		"slug": "proj-tenant", "project_id": "proj-abc123",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["project_id"] != "proj-abc123" {
		t.Errorf("expected project_id=proj-abc123 in response, got %q", body["project_id"])
	}

	// Also verify the tenant was persisted correctly.
	got := getJSON(t, ts, "/v1/tenants/tnt_proj-tenant")
	defer got.Body.Close()
	var tenant map[string]any
	_ = json.NewDecoder(got.Body).Decode(&tenant)
	if tenant["project_id"] != "proj-abc123" {
		t.Errorf("GET tenant: expected project_id=proj-abc123, got %v", tenant["project_id"])
	}
}

// TestHandleCreateTenant_InvalidProjectID verifies that an invalid project_id
// returns HTTP 400 (spec scenario "Reject invalid project_id").
func TestHandleCreateTenant_InvalidProjectID(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	cases := []string{
		"has space",
		"has@symbol",
		"has.dot",
		"has/slash",
		"tooLong_" + strings.Repeat("x", 60), // > 64 chars
	}
	for _, pid := range cases {
		resp := postJSON(t, ts, "/v1/tenants", map[string]string{
			"slug": "slug-" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(pid[:min(len(pid), 5)], " ", ""), "@", "")),
			"project_id": pid,
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("project_id %q: expected 400, got %d", pid, resp.StatusCode)
		}
	}
}

// TestHandleUpdateTenant_ProjectID verifies that PATCH updates project_id and
// returns the updated tenant (spec scenario "Update project_id on existing tenant").
func TestHandleUpdateTenant_ProjectID(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "patch-tenant", "project_id": "proj-abc"}).Body.Close()

	resp := patchJSON(t, ts, "/v1/tenants/tnt_patch-tenant", map[string]string{
		"project_id": "proj-xyz456",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["project_id"] != "proj-xyz456" {
		t.Errorf("expected project_id=proj-xyz456 in PATCH response, got %v", body["project_id"])
	}
}

// TestHandleUpdateTenant_InvalidProjectID verifies that PATCH with an invalid
// project_id returns HTTP 400.
func TestHandleUpdateTenant_InvalidProjectID(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "patch-bad"}).Body.Close()

	resp := patchJSON(t, ts, "/v1/tenants/tnt_patch-bad", map[string]string{
		"project_id": "invalid id!",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid project_id, got %d", resp.StatusCode)
	}
}

// TestHandleListTenants_FilterByProjectID verifies that ?project_id= filter returns
// only tenants with that project_id (spec scenario "Filter tenants by project_id").
func TestHandleListTenants_FilterByProjectID(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "a1", "project_id": "proj-target"}).Body.Close()
	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "a2", "project_id": "proj-target"}).Body.Close()
	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "a3", "project_id": "proj-other"}).Body.Close()
	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "a4"}).Body.Close()

	resp := getJSON(t, ts, "/v1/tenants?project_id=proj-target")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var rows []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) != 2 {
		t.Errorf("expected 2 tenants with proj-target, got %d", len(rows))
	}
	for _, r := range rows {
		if r["project_id"] != "proj-target" {
			t.Errorf("unexpected project_id %v in filtered result", r["project_id"])
		}
	}
}

// TestHandleListTenants_FilterUntagged verifies that ?project_id= (empty) returns
// only untagged tenants (spec scenario "List untagged tenants").
func TestHandleListTenants_FilterUntagged(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "b1"}).Body.Close()
	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "b2", "project_id": "proj-tagged"}).Body.Close()

	resp := getJSON(t, ts, "/v1/tenants?project_id=")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var rows []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	for _, r := range rows {
		if pid, _ := r["project_id"].(string); pid != "" {
			t.Errorf("expected only untagged tenants, got tenant with project_id=%q", pid)
		}
	}
	if len(rows) < 1 {
		t.Error("expected at least one untagged tenant")
	}
}

// TestHandleListTenants_NoFilter verifies that omitting ?project_id= returns all
// tenants (spec scenario "Unfiltered list returns all tenants").
func TestHandleListTenants_NoFilter(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "c1"}).Body.Close()
	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "c2", "project_id": "proj-any"}).Body.Close()

	resp := getJSON(t, ts, "/v1/tenants")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var rows []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) < 2 {
		t.Errorf("expected at least 2 tenants with no filter, got %d", len(rows))
	}
}

// TestHandleGetTenant_IncludesProjectID verifies that GET /v1/tenants/:id includes
// project_id in the response JSON (task 3.4).
func TestHandleGetTenant_IncludesProjectID(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "get-proj", "project_id": "proj-get123"}).Body.Close()

	resp := getJSON(t, ts, "/v1/tenants/tnt_get-proj")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["project_id"] != "proj-get123" {
		t.Errorf("expected project_id=proj-get123, got %v", body["project_id"])
	}
}

// TestHandleLegacyTenant_OmitsProjectID verifies that a tenant without a project_id
// does NOT include the "project_id" key in its JSON response (omitempty, task 6.3).
func TestHandleLegacyTenant_OmitsProjectID(t *testing.T) {
	store := openTestStore(t)
	ts := newTestServer(t, store)
	defer ts.Close()

	postJSON(t, ts, "/v1/tenants", map[string]string{"slug": "legacy-tenant"}).Body.Close()

	resp := getJSON(t, ts, "/v1/tenants/tnt_legacy-tenant")
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)

	// project_id must be absent (omitempty) for backward compatibility.
	if _, ok := body["project_id"]; ok {
		t.Errorf("expected project_id to be absent for untagged tenant (omitempty), but it was present: %v", body["project_id"])
	}
}

// min is a small helper for computing minimum int; avoids importing slices.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
