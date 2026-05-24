package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"controlai/internal/store/sqlite"
)

func openTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCreateAndGetTenant(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	err := store.CreateTenant(ctx, sqlite.TenantRow{
		ID:            "tnt_acme-corp",
		Name:          "Acme Corp",
		Domain:        "acme.example.com",
		Retention:     "7d",
		SchemaVersion: 1,
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	got, err := store.GetTenant(ctx, "tnt_acme-corp")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	if got.ID != "tnt_acme-corp" {
		t.Errorf("expected ID tnt_acme-corp, got %s", got.ID)
	}
	if got.Status != "active" {
		t.Errorf("expected status active, got %s", got.Status)
	}
}

func TestDuplicateTenantReturnsErrDuplicate(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	row := sqlite.TenantRow{ID: "tnt_dup", Domain: "d.example.com", Retention: "1d", SchemaVersion: 1}
	if err := store.CreateTenant(ctx, row); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := store.CreateTenant(ctx, row)
	if err != sqlite.ErrDuplicate {
		t.Errorf("expected ErrDuplicate, got %v", err)
	}
}

func TestGetTenantNotFound(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	_, err := store.GetTenant(ctx, "tnt_missing")
	if err != sqlite.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteTenantSoft(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_x", Domain: "x.com", Retention: "1d", SchemaVersion: 1})

	if err := store.DeleteTenant(ctx, "tnt_x", false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	got, err := store.GetTenant(ctx, "tnt_x")
	if err != nil {
		t.Fatalf("get after soft delete: %v", err)
	}
	if got.Status != "orphaned" {
		t.Errorf("expected status orphaned after soft delete, got %s", got.Status)
	}
}

func TestDeleteTenantHard(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_y", Domain: "y.com", Retention: "1d", SchemaVersion: 1})

	if err := store.DeleteTenant(ctx, "tnt_y", true); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	_, err := store.GetTenant(ctx, "tnt_y")
	if err != sqlite.ErrNotFound {
		t.Errorf("expected ErrNotFound after hard delete, got %v", err)
	}
}

func TestCreateSiteAndForeignKey(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Site without tenant should fail FK constraint.
	err := store.CreateSite(ctx, sqlite.SiteRow{
		ID:            "ste_seoul",
		TenantID:      "tnt_nonexistent",
		BrokerKind:    "mosquitto",
		Throughput:    "low",
		Direction:     "uni",
		PayloadCodec:  "cbor",
		LeafTTLDays:   365,
		SchemaVersion: 1,
	})
	if err == nil {
		t.Error("expected FK violation error when tenant does not exist")
	}
}

func TestCreateSiteSuccess(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_t1", Domain: "t1.com", Retention: "7d", SchemaVersion: 1})

	err := store.CreateSite(ctx, sqlite.SiteRow{
		ID: "ste_seoul", TenantID: "tnt_t1",
		BrokerKind: "mosquitto", Throughput: "low", Direction: "uni",
		PayloadCodec: "cbor", LeafTTLDays: 365, SchemaVersion: 1,
	})
	if err != nil {
		t.Fatalf("create site: %v", err)
	}

	got, err := store.GetSite(ctx, "ste_seoul")
	if err != nil {
		t.Fatalf("get site: %v", err)
	}
	if got.TenantID != "tnt_t1" {
		t.Errorf("expected tenant tnt_t1, got %s", got.TenantID)
	}
}

func TestCreateAndRevokeToken(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	raw, row, err := store.CreateToken(ctx, "test-token")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if len(raw) == 0 {
		t.Error("expected non-empty raw token")
	}
	if row.Prefix != raw[:8] {
		t.Errorf("prefix mismatch: got %s, want %s", row.Prefix, raw[:8])
	}

	// Lookup should succeed.
	found, err := store.LookupToken(ctx, raw)
	if err != nil {
		t.Fatalf("lookup token: %v", err)
	}
	if found.DisplayName != "test-token" {
		t.Errorf("display name mismatch")
	}

	// Revoke.
	if err := store.RevokeToken(ctx, row.ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	// Lookup after revoke should fail.
	_, err = store.LookupToken(ctx, raw)
	if err != sqlite.ErrNotFound {
		t.Errorf("expected ErrNotFound after revocation, got %v", err)
	}
}

func TestUpsertAndGetDesiredState(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	err := store.UpsertDesiredState(ctx, sqlite.DesiredStateRow{
		ProjectID: "tnt_t1-ste_s1",
		TenantID:  "tnt_t1",
		SiteID:    "ste_s1",
		State:     "running",
		ConfigHash: "abc123",
	})
	if err != nil {
		t.Fatalf("upsert desired state: %v", err)
	}
	got, err := store.GetDesiredState(ctx, "tnt_t1-ste_s1")
	if err != nil {
		t.Fatalf("get desired state: %v", err)
	}
	if got.State != "running" {
		t.Errorf("expected state running, got %s", got.State)
	}

	// Update to stopped.
	_ = store.UpsertDesiredState(ctx, sqlite.DesiredStateRow{ProjectID: "tnt_t1-ste_s1", State: "stopped"})
	got, _ = store.GetDesiredState(ctx, "tnt_t1-ste_s1")
	if got.State != "stopped" {
		t.Errorf("expected stopped after update, got %s", got.State)
	}
}

// Ensure the migrations SQL file is embedded and runnable (tested implicitly
// by Open succeeding above). This test verifies the file exists on disk.
func TestMigrationFileExists(t *testing.T) {
	path := "migrations/0001_init.sql"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("migration file not found at %s", path)
	}
}

func TestStoreCACertRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Create a tenant and site first (FK constraints).
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_pki", Domain: "pki.com", Retention: "7d", SchemaVersion: 1})
	_ = store.CreateSite(ctx, sqlite.SiteRow{
		ID: "ste_ca", TenantID: "tnt_pki",
		BrokerKind: "mosquitto", Throughput: "low", Direction: "uni",
		PayloadCodec: "cbor", LeafTTLDays: 365, SchemaVersion: 1,
	})

	row := sqlite.CACertRow{
		SiteID:      "ste_ca",
		CertPEM:     "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		KeyEnc:      []byte{0x01, 0x02, 0x03},
		KeyNonce:    []byte{0x04, 0x05, 0x06},
		Fingerprint: "abc123fingerprint",
	}
	// Use fixed times that avoid zero value.
	row.NotBefore = parseTime(t, "2024-01-01T00:00:00Z")
	row.NotAfter = parseTime(t, "2044-01-01T00:00:00Z")

	if err := store.StoreCACert(ctx, row); err != nil {
		t.Fatalf("StoreCACert: %v", err)
	}

	got, err := store.GetCACert(ctx, "ste_ca")
	if err != nil {
		t.Fatalf("GetCACert: %v", err)
	}
	if got.Fingerprint != row.Fingerprint {
		t.Errorf("fingerprint mismatch: got %s, want %s", got.Fingerprint, row.Fingerprint)
	}
	if string(got.KeyEnc) != string(row.KeyEnc) {
		t.Errorf("key_enc mismatch")
	}
}

func TestGetCACert_NotFound(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	_, err := store.GetCACert(ctx, "ste_missing")
	if err != sqlite.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreCertAndRevoke(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_c", Domain: "c.com", Retention: "7d", SchemaVersion: 1})
	_ = store.CreateSite(ctx, sqlite.SiteRow{
		ID: "ste_c", TenantID: "tnt_c",
		BrokerKind: "mosquitto", Throughput: "low", Direction: "uni",
		PayloadCodec: "cbor", LeafTTLDays: 365, SchemaVersion: 1,
	})

	row := sqlite.CertRow{
		SiteID:      "ste_c",
		Kind:        "gateway",
		CommonName:  "floor-1-pump",
		CertPEM:     "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		Fingerprint: "deadbeef1234567890",
		NotBefore:   parseTime(t, "2024-01-01T00:00:00Z"),
		NotAfter:    parseTime(t, "2025-01-01T00:00:00Z"),
	}
	if err := store.StoreCert(ctx, row); err != nil {
		t.Fatalf("StoreCert: %v", err)
	}

	// GetByFingerprint
	got, err := store.GetCertByFingerprint(ctx, row.Fingerprint)
	if err != nil {
		t.Fatalf("GetCertByFingerprint: %v", err)
	}
	if got.CommonName != "floor-1-pump" {
		t.Errorf("CN mismatch: %s", got.CommonName)
	}
	if got.Revoked {
		t.Error("expected cert not revoked initially")
	}

	// Revoke
	if err := store.RevokeCert(ctx, row.Fingerprint); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}

	// After revocation, ListCertsBySite should not return it.
	certs, err := store.ListCertsBySite(ctx, "ste_c", "gateway")
	if err != nil {
		t.Fatalf("ListCertsBySite: %v", err)
	}
	for _, c := range certs {
		if c.Fingerprint == row.Fingerprint {
			t.Error("revoked cert should not appear in ListCertsBySite(revoked=0)")
		}
	}
}

func TestRevokeCert_NotFound(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	err := store.RevokeCert(ctx, "nonexistent_fingerprint")
	if err != sqlite.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// parseTime is a test helper to parse RFC3339 times.
func parseTime(t *testing.T, s string) (ts time.Time) {
	t.Helper()
	var err error
	ts, err = time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parseTime %q: %v", s, err)
	}
	return ts
}

// ─── project_id tests ─────────────────────────────────────────────────────────

// TestCreateTenantWithProjectID verifies that a tenant created with a project_id
// round-trips through the store correctly (tasks 5.1, 2.1–2.3).
func TestCreateTenantWithProjectID(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	err := store.CreateTenant(ctx, sqlite.TenantRow{
		ID:            "tnt_proj-tenant",
		Domain:        "proj.example.com",
		Retention:     "7d",
		SchemaVersion: 1,
		ProjectID:     "proj-abc",
	})
	if err != nil {
		t.Fatalf("create tenant with project_id: %v", err)
	}

	got, err := store.GetTenant(ctx, "tnt_proj-tenant")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	if got.ProjectID != "proj-abc" {
		t.Errorf("expected project_id=proj-abc, got %q", got.ProjectID)
	}
}

// TestCreateTenantWithoutProjectID verifies that a tenant created without a
// project_id has an empty ProjectID field (task 5.2).
func TestCreateTenantWithoutProjectID(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	err := store.CreateTenant(ctx, sqlite.TenantRow{
		ID:            "tnt_no-proj",
		Domain:        "noproj.example.com",
		Retention:     "7d",
		SchemaVersion: 1,
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	got, err := store.GetTenant(ctx, "tnt_no-proj")
	if err != nil {
		t.Fatalf("get tenant: %v", err)
	}
	if got.ProjectID != "" {
		t.Errorf("expected empty project_id, got %q", got.ProjectID)
	}
}

// TestUpdateTenantProjectID verifies that UpdateTenantProjectID updates the tag
// and GetTenant reflects the new value (task 5.5).
func TestUpdateTenantProjectID(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	_ = store.CreateTenant(ctx, sqlite.TenantRow{
		ID: "tnt_update-proj", Domain: "up.example.com", Retention: "1d", SchemaVersion: 1,
		ProjectID: "proj-abc",
	})

	if err := store.UpdateTenantProjectID(ctx, "tnt_update-proj", "proj-xyz"); err != nil {
		t.Fatalf("UpdateTenantProjectID: %v", err)
	}

	got, err := store.GetTenant(ctx, "tnt_update-proj")
	if err != nil {
		t.Fatalf("get tenant after update: %v", err)
	}
	if got.ProjectID != "proj-xyz" {
		t.Errorf("expected project_id=proj-xyz after update, got %q", got.ProjectID)
	}
}

// TestUpdateTenantProjectID_NotFound verifies ErrNotFound is returned for missing tenant.
func TestUpdateTenantProjectID_NotFound(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	err := store.UpdateTenantProjectID(ctx, "tnt_ghost", "proj-x")
	if err != sqlite.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestListTenantsFiltered_ByProjectID verifies that ListTenantsFiltered with a
// non-empty filter returns only matching tenants (task 5.3).
func TestListTenantsFiltered_ByProjectID(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// Create three tenants: two tagged, one untagged.
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_fa", Domain: "fa.com", Retention: "1d", SchemaVersion: 1, ProjectID: "proj-abc123"})
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_fb", Domain: "fb.com", Retention: "1d", SchemaVersion: 1, ProjectID: "proj-abc123"})
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_fc", Domain: "fc.com", Retention: "1d", SchemaVersion: 1, ProjectID: "proj-other"})
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_fd", Domain: "fd.com", Retention: "1d", SchemaVersion: 1})

	filter := "proj-abc123"
	rows, err := store.ListTenantsFiltered(ctx, &filter)
	if err != nil {
		t.Fatalf("ListTenantsFiltered: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 tenants with proj-abc123, got %d", len(rows))
	}
	for _, r := range rows {
		if r.ProjectID != "proj-abc123" {
			t.Errorf("unexpected tenant %s with project_id=%q in filtered result", r.ID, r.ProjectID)
		}
	}
}

// TestListTenantsFiltered_Untagged verifies that empty filter returns NULL and ''
// project_id tenants only (spec: "List untagged tenants").
func TestListTenantsFiltered_Untagged(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_ga", Domain: "ga.com", Retention: "1d", SchemaVersion: 1})
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_gb", Domain: "gb.com", Retention: "1d", SchemaVersion: 1, ProjectID: "proj-tagged"})

	emptyFilter := ""
	rows, err := store.ListTenantsFiltered(ctx, &emptyFilter)
	if err != nil {
		t.Fatalf("ListTenantsFiltered(empty): %v", err)
	}
	for _, r := range rows {
		if r.ProjectID != "" {
			t.Errorf("expected only untagged tenants, but got tenant %s with project_id=%q", r.ID, r.ProjectID)
		}
	}
	if len(rows) < 1 {
		t.Error("expected at least one untagged tenant")
	}
}

// TestListTenantsFiltered_Nil verifies that nil filter returns all tenants (task 5.4).
func TestListTenantsFiltered_Nil(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_ha", Domain: "ha.com", Retention: "1d", SchemaVersion: 1})
	_ = store.CreateTenant(ctx, sqlite.TenantRow{ID: "tnt_hb", Domain: "hb.com", Retention: "1d", SchemaVersion: 1, ProjectID: "proj-any"})

	rows, err := store.ListTenantsFiltered(ctx, nil)
	if err != nil {
		t.Fatalf("ListTenantsFiltered(nil): %v", err)
	}
	if len(rows) < 2 {
		t.Errorf("expected all tenants with nil filter, got %d", len(rows))
	}
}

// TestMigration0002_BackwardCompatibility verifies that an existing database
// (0001 schema) gains project_id=NULL for existing tenants after the migration,
// and that ListTenants still returns them (tasks 6.1, 6.2, 6.3).
func TestMigration0002_BackwardCompatibility(t *testing.T) {
	// Opening a store already applies both migrations. We verify that:
	// (a) the store opens without error (migration applied)
	// (b) tenants created without project_id have empty ProjectID
	// (c) ListTenants returns them (NULL project_id not excluded)
	ctx := context.Background()
	store := openTestStore(t)

	_ = store.CreateTenant(ctx, sqlite.TenantRow{
		ID: "tnt_legacy", Domain: "legacy.example.com", Retention: "7d", SchemaVersion: 1,
	})

	tenants, err := store.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants after migration: %v", err)
	}
	var found bool
	for _, tn := range tenants {
		if tn.ID == "tnt_legacy" {
			found = true
			// ProjectID should be empty string (COALESCE of NULL)
			if tn.ProjectID != "" {
				t.Errorf("legacy tenant should have empty project_id, got %q", tn.ProjectID)
			}
		}
	}
	if !found {
		t.Error("legacy tenant not returned by ListTenants")
	}
}

// TestMigrationFileExists_0002 verifies the 0002 migration file is present.
func TestMigrationFileExists_0002(t *testing.T) {
	path := "migrations/0002_add_project_id.sql"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("migration file not found at %s", path)
	}
}

// TestMigration0002_Idempotent verifies that opening a store twice (re-running
// migration) does not fail (task 1.3 guard check).
func TestMigration0002_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/idempotent.db"

	store1, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = store1.Close()

	store2, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("second open (idempotency): %v", err)
	}
	_ = store2.Close()
}

// ─── Task 13.5 Verification: purge delete removes all artifacts ────────────────

// TestPurgeDelete_TenantRowRemovedCompletely verifies that controlai tenant rm --purge
// (purge=true) completely removes the tenant row from SQLite so that subsequent
// reads return ErrNotFound.  This is the database-layer assertion for the spec
// scenario "controlai tenant rm tnt_acme-corp --purge removes containers +
// volumes + on-disk directory + tenant row from SQLite."
func TestPurgeDelete_TenantRowRemovedCompletely(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	const tenantID = "tnt_purge-test"
	if err := store.CreateTenant(ctx, sqlite.TenantRow{
		ID:            tenantID,
		Name:          "Purge Test",
		Domain:        "purge.example.com",
		Retention:     "7d",
		SchemaVersion: 1,
	}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// Verify tenant exists before purge.
	got, err := store.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("get before purge: %v", err)
	}
	if got.ID != tenantID {
		t.Fatalf("tenant ID mismatch: %s", got.ID)
	}

	// Hard delete (purge=true) — models `controlai tenant rm --purge`.
	if err := store.DeleteTenant(ctx, tenantID, true); err != nil {
		t.Fatalf("hard delete (purge): %v", err)
	}

	// After purge, GetTenant must return ErrNotFound.
	_, err = store.GetTenant(ctx, tenantID)
	if err != sqlite.ErrNotFound {
		t.Errorf("expected ErrNotFound after purge, got %v", err)
	}

	// ListTenants must not include the purged tenant.
	tenants, err := store.ListTenants(ctx)
	if err != nil {
		t.Fatalf("list after purge: %v", err)
	}
	for _, tn := range tenants {
		if tn.ID == tenantID {
			t.Errorf("purged tenant %s must not appear in ListTenants", tenantID)
		}
	}
}

// TestPurgeDelete_SitesRemovedWithTenant verifies that all sites belonging to a
// purged tenant are also removed via cascade.  The spec requires:
//   "controlai SHALL stop all related containers, remove all related volumes,
//    delete /var/lib/controlai/tenants/<id>/ … and remove the tenant row from SQLite"
//
// The SQLite layer must enforce cascade deletion of sites when a tenant is purged.
func TestPurgeDelete_SitesRemovedWithTenant(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	const tenantID = "tnt_cascade-purge"
	if err := store.CreateTenant(ctx, sqlite.TenantRow{
		ID:            tenantID,
		Domain:        "cascade.example.com",
		Retention:     "1d",
		SchemaVersion: 1,
	}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	// Add two sites under the tenant.
	for _, siteID := range []string{"ste_alpha", "ste_beta"} {
		if err := store.CreateSite(ctx, sqlite.SiteRow{
			ID:            siteID,
			TenantID:      tenantID,
			BrokerKind:    "mosquitto",
			Throughput:    "low",
			Direction:     "uni",
			PayloadCodec:  "cbor",
			LeafTTLDays:   365,
			SchemaVersion: 1,
		}); err != nil {
			t.Fatalf("create site %s: %v", siteID, err)
		}
	}

	// Confirm sites exist.
	sites, err := store.ListSitesByTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("list sites before purge: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("expected 2 sites before purge, got %d", len(sites))
	}

	// Purge the tenant.
	if err := store.DeleteTenant(ctx, tenantID, true); err != nil {
		t.Fatalf("purge tenant: %v", err)
	}

	// All sites must be gone.
	sitesAfter, err := store.ListSitesByTenant(ctx, tenantID)
	if err != nil && err != sqlite.ErrNotFound {
		t.Fatalf("list sites after purge: %v", err)
	}
	if len(sitesAfter) != 0 {
		t.Errorf("expected 0 sites after tenant purge, got %d (cascade delete missing?)", len(sitesAfter))
	}
}

// TestSoftDelete_PreservesDataAsOrphaned verifies the conservative delete path:
// `controlai tenant rm` without --purge sets status=orphaned but does not remove
// the tenant row or its sites (data is preserved for recovery).
func TestSoftDelete_PreservesDataAsOrphaned(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	const tenantID = "tnt_soft-delete"
	if err := store.CreateTenant(ctx, sqlite.TenantRow{
		ID:            tenantID,
		Domain:        "soft.example.com",
		Retention:     "7d",
		SchemaVersion: 1,
	}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := store.CreateSite(ctx, sqlite.SiteRow{
		ID:            "ste_keep",
		TenantID:      tenantID,
		BrokerKind:    "mosquitto",
		Throughput:    "low",
		Direction:     "uni",
		PayloadCodec:  "cbor",
		LeafTTLDays:   365,
		SchemaVersion: 1,
	}); err != nil {
		t.Fatalf("create site: %v", err)
	}

	// Soft delete (purge=false).
	if err := store.DeleteTenant(ctx, tenantID, false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// Tenant must still exist as orphaned.
	got, err := store.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("get after soft delete: %v", err)
	}
	if got.Status != "orphaned" {
		t.Errorf("expected status=orphaned, got %q", got.Status)
	}

	// Sites must still be present (data preserved for recovery).
	sites, err := store.ListSitesByTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("list sites after soft delete: %v", err)
	}
	if len(sites) == 0 {
		t.Error("sites must be preserved after soft delete; data loss is unacceptable without --purge")
	}
}
