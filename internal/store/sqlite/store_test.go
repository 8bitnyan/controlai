package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
