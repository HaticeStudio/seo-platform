// Package providertest is the public contract-test kit provider packages run
// against their implementations, plus a configurable fake provider the
// runtime's own tests (and host integration tests) reuse.
package providertest

import (
	"context"
	"net/url"
	"sync"
	"testing"

	"github.com/HaticeStudio/seo-platform/core"
)

// RunContract asserts the invariants every provider must hold. Provider
// packages call it from their own tests with a ready-to-use instance.
func RunContract(t *testing.T, p core.Provider) {
	t.Helper()
	d := p.Descriptor()
	if d.Name == "" {
		t.Fatal("descriptor name must not be empty")
	}
	if d.DisplayName == "" {
		t.Error("descriptor display name must not be empty")
	}
	if len(d.Capabilities) == 0 {
		t.Fatal("provider must declare at least one capability")
	}
	seen := map[core.Capability]bool{}
	for _, spec := range d.Capabilities {
		if spec.Capability == "" {
			t.Fatal("capability name must not be empty")
		}
		if seen[spec.Capability] {
			t.Fatalf("capability %q declared twice", spec.Capability)
		}
		seen[spec.Capability] = true
		if spec.MaxRangeDays < 0 || spec.FreshnessLagDays < 0 {
			t.Fatalf("capability %q declares negative limits", spec.Capability)
		}
	}
	for _, link := range d.SetupLinks {
		parsed, err := url.Parse(link.URL)
		if link.Label == "" || err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			t.Errorf("invalid setup link: %+v", link)
		}
	}
}

// Fake is an in-memory provider for tests. Behavior is injected per method;
// unset hooks succeed with zero values.
type Fake struct {
	Desc         core.Descriptor
	DiscoverFunc func(ctx context.Context, credential core.CredentialHandle) ([]core.Property, error)
	SyncFunc     func(ctx context.Context, req core.SyncRequest, credential core.CredentialHandle, sink core.SnapshotSink) (core.SyncResult, error)
	TestFunc     func(ctx context.Context) error
	RevokeFunc   func(ctx context.Context) error

	mu        sync.Mutex
	SyncCalls []core.SyncRequest
}

// NewFake returns a fake provider declaring one search.performance capability.
func NewFake(name string) *Fake {
	return &Fake{Desc: core.Descriptor{
		Name:            name,
		DisplayName:     "Fake " + name,
		CredentialTypes: []string{"api_key"},
		Capabilities: []core.CapabilitySpec{{
			Capability:       core.CapSearchPerformance,
			Metrics:          []string{"clicks", "impressions"},
			MaxRangeDays:     366,
			FreshnessLagDays: 1,
			SupportsCursor:   true,
		}},
	}}
}

func (f *Fake) Descriptor() core.Descriptor { return f.Desc }

func (f *Fake) DiscoverProperties(ctx context.Context, credential core.CredentialHandle) ([]core.Property, error) {
	if f.DiscoverFunc != nil {
		return f.DiscoverFunc(ctx, credential)
	}
	return []core.Property{{Reference: "fake-property", DisplayName: "Fake property"}}, nil
}

func (f *Fake) Test(ctx context.Context, _ core.Site, _ core.Property, _ core.CredentialHandle) error {
	if f.TestFunc != nil {
		return f.TestFunc(ctx)
	}
	return nil
}

func (f *Fake) Sync(ctx context.Context, req core.SyncRequest, credential core.CredentialHandle, sink core.SnapshotSink) (core.SyncResult, error) {
	f.mu.Lock()
	f.SyncCalls = append(f.SyncCalls, req)
	f.mu.Unlock()
	if f.SyncFunc != nil {
		return f.SyncFunc(ctx, req, credential, sink)
	}
	return core.SyncResult{}, nil
}

func (f *Fake) Revoke(ctx context.Context, _ core.CredentialHandle) error {
	if f.RevokeFunc != nil {
		return f.RevokeFunc(ctx)
	}
	return nil
}

// Calls returns a snapshot of recorded sync requests.
func (f *Fake) Calls() []core.SyncRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.SyncRequest(nil), f.SyncCalls...)
}
