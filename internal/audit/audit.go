// Package audit provides structured audit-event emission used across controlai.
// Events are persisted in the SQLite audit_events table via the store package.
package audit

import (
	"context"
	"time"
)

// Kind is a dot-separated audit event kind, e.g. "reconciler.success".
type Kind string

const (
	KindTenantCreate      Kind = "tenant.create"
	KindTenantUpdate      Kind = "tenant.update"
	KindTenantDelete      Kind = "tenant.delete"
	KindTenantMigrate     Kind = "tenant.migrate"
	KindSiteCreate        Kind = "site.create"
	KindSiteUpdate        Kind = "site.update"
	KindSiteDelete        Kind = "site.delete"
	KindSiteStart         Kind = "site.start"
	KindSiteStop          Kind = "site.stop"
	KindReconcilerSuccess Kind = "reconciler.success"
	KindReconcilerFailure Kind = "reconciler.failure"
	KindReconcilerBackoff Kind = "reconciler.backoff"
	KindBrokerRestart     Kind = "broker.listener_restart"
	KindPKIRotate         Kind = "pki.rotate"
	KindMigrateApply      Kind = "migrate.apply"
	KindTokenCreate       Kind = "token.create"
	KindTokenRevoke       Kind = "token.revoke"
)

// Event represents a single audit record.
type Event struct {
	ID        int64     `db:"id"`
	Kind      Kind      `db:"kind"`
	TenantID  string    `db:"tenant_id"` // may be empty
	SiteID    string    `db:"site_id"`   // may be empty
	ActorIP   string    `db:"actor_ip"`  // may be empty
	Detail    string    `db:"detail"`    // JSON or free-form text
	Success   bool      `db:"success"`
	CreatedAt time.Time `db:"created_at"`
}

// Emitter is the interface the store must satisfy so other packages can emit
// audit events without a direct import cycle.
type Emitter interface {
	Emit(ctx context.Context, ev Event) error
}

// NoopEmitter discards all events; useful in unit tests.
type NoopEmitter struct{}

func (NoopEmitter) Emit(_ context.Context, _ Event) error { return nil }
