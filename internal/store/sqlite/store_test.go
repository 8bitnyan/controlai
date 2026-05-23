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
