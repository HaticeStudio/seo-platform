// Package core defines the public contracts of the SEO platform runtime.
//
// Everything in this package is host-neutral: no tenant product, IAM vendor,
// deployment platform, or customer assumption may appear here (ADR 0005).
package core

import "time"

// Site is the unit everything else hangs off. Single-site deployments have
// exactly one; multi-site is an optional extension resolved by a
// WorkspaceResolver and never required by this model.
type Site struct {
	ID         string
	PublicURL  string
	SitemapURL string
	Timezone   string
}

// ProviderConnection is the operational state of one provider on one site.
// Credential material deliberately has no field here; only an opaque
// CredentialRef may be persisted.
type ProviderConnection struct {
	ID            string
	SiteID        string
	Provider      string
	CredentialRef CredentialRef
	Enabled       bool
	// PropertyReference is the provider-side property the administrator chose
	// (e.g. a Search Console property URL or a GA4 property ID).
	PropertyReference string
	LastSuccessAt     *time.Time
	DataThroughDate   *time.Time
	LastErrorCode     ErrorCode
	LastErrorMessage  string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Property is one provider-side property visible to a credential. A single
// OAuth grant may see several properties; choosing one must not copy the
// credential, which is why Property carries the connection ID, not a secret.
type Property struct {
	ConnectionID string
	Reference    string
	DisplayName  string
	Timezone     string
}

// SyncRunStatus is the lifecycle of one sync run.
type SyncRunStatus string

const (
	SyncQueued    SyncRunStatus = "QUEUED"
	SyncRunning   SyncRunStatus = "RUNNING"
	SyncSucceeded SyncRunStatus = "SUCCEEDED"
	SyncPartial   SyncRunStatus = "PARTIAL"
	SyncFailed    SyncRunStatus = "FAILED"
)

// SyncRun records one execution of one capability over a date range.
// It never stores authorization material.
type SyncRun struct {
	ID             string
	Provider       string
	Capability     Capability
	StartDate      time.Time
	EndDate        time.Time
	Status         SyncRunStatus
	RowsSynced     int64
	Cursor         string
	TriggeredBy    string // opaque subject ID; empty for the scheduler
	IdempotencyKey string
	ErrorCode      ErrorCode
	ErrorMessage   string
	StartedAt      time.Time
	FinishedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AuditEvent is an append-only operational record. It intentionally has no
// field for request bodies, secrets, or raw provider payloads.
type AuditEvent struct {
	ID        string
	Actor     string
	Action    string
	Target    string
	Result    string
	RequestID string
	At        time.Time
}
