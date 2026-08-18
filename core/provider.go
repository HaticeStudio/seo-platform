package core

import (
	"context"
	"time"
)

// Capability names one thing a provider can do. Providers declare what they
// support; the Console renders from these declarations instead of assuming
// every platform has equivalent fields. Published names must never be
// re-interpreted (VERSIONING.md).
type Capability string

const (
	CapSearchPerformance Capability = "search.performance"
	CapURLInspection     Capability = "index.url_inspection"
	CapSitemaps          Capability = "index.sitemaps"
	CapCrawlStats        Capability = "index.crawl_stats"
	CapAcquisition       Capability = "analytics.acquisition"
	CapConversion        Capability = "analytics.conversion"
)

// CapabilitySpec declares the real shape of one capability on one provider.
// A missing capability yields UNSUPPORTED — never a row of fabricated zeros.
type CapabilitySpec struct {
	Capability Capability
	Dimensions []string
	Metrics    []string
	// MaxRangeDays is the widest date range one sync may request.
	MaxRangeDays int
	// FreshnessLagDays is how far behind "today" the provider's data runs
	// (e.g. Search Console lags ~3 days). The scheduler clamps end dates by it.
	FreshnessLagDays int
	// SupportsCursor reports whether interrupted syncs can resume from a
	// checkpoint instead of restarting the whole range.
	SupportsCursor bool
	// QuotaHint is a human-readable note about provider rate limits.
	QuotaHint string
}

// Descriptor identifies a provider and declares its capabilities plus the
// deep links the Console shows. URLs live here — in provider code shipped with
// the provider — precisely so the UI never hardcodes third-party paths.
type Descriptor struct {
	// Name is the stable registry key, e.g. "google-search-console".
	Name        string
	DisplayName string
	// CredentialTypes lists accepted credential kinds, e.g. "oauth2",
	// "service_account_json", "api_key".
	CredentialTypes []string
	Capabilities    []CapabilitySpec
	// SetupURL deep-links the third-party console where an administrator
	// creates credentials.
	SetupURL string
	DocsURL  string
}

// Spec returns the declaration for one capability, if the provider has it.
func (d Descriptor) Spec(c Capability) (CapabilitySpec, bool) {
	for _, s := range d.Capabilities {
		if s.Capability == c {
			return s, true
		}
	}
	return CapabilitySpec{}, false
}

// SyncRequest asks a provider to sync one capability over a date range.
type SyncRequest struct {
	Site       Site
	Property   Property
	Capability Capability
	StartDate  time.Time
	EndDate    time.Time
	// Cursor resumes a previously interrupted run; empty starts fresh.
	Cursor string
}

// SyncResult reports what a completed (possibly partial) sync achieved.
type SyncResult struct {
	Rows int64
	// Cursor is the resume point when the run stopped early; empty when the
	// range completed.
	Cursor string
	// DataThroughDate is the last date the provider had real data for. It is
	// how the UI distinguishes "truly zero" from "not synced yet".
	DataThroughDate time.Time
}

// SnapshotSink is where providers write normalized rows. The runtime owns the
// transaction and checkpointing so providers never depend on a specific
// database or ORM.
type SnapshotSink interface {
	// Write stores one batch of normalized rows for the given dataset within
	// the run's transaction. Datasets are provider-declared table names in the
	// normalized report store.
	Write(ctx context.Context, dataset string, rows []map[string]any) error
	// Checkpoint durably records the cursor so a later run can resume. It must
	// commit everything written since the previous checkpoint.
	Checkpoint(ctx context.Context, cursor string) error
}

// Provider is the boundary every integration implements. Implementations live
// in providers/* and depend only on this package.
type Provider interface {
	Descriptor() Descriptor
	// DiscoverProperties lists provider-side properties the credential can see.
	DiscoverProperties(ctx context.Context, credential CredentialHandle) ([]Property, error)
	// Test verifies the credential works for the chosen property without
	// syncing data.
	Test(ctx context.Context, site Site, property Property, credential CredentialHandle) error
	// Sync pulls the requested range and writes normalized snapshots to sink.
	// Failures must be returned as *SyncError with a classification.
	Sync(ctx context.Context, request SyncRequest, credential CredentialHandle, sink SnapshotSink) (SyncResult, error)
	// Revoke invalidates the credential provider-side (e.g. OAuth token
	// revocation). Secret deletion is the SecretStore's job, not the provider's.
	Revoke(ctx context.Context, credential CredentialHandle) error
}
