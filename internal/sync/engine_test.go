package sync

import (
	"context"
	"testing"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/registry"
	"github.com/HaticeStudio/seo-platform/internal/secrets"
	"github.com/HaticeStudio/seo-platform/internal/store"
	"github.com/HaticeStudio/seo-platform/providertest"
)

const fakeProvider = "fake-search"

type fixture struct {
	engine  *Engine
	store   *store.Store
	fake    *providertest.Fake
	secrets *secrets.Memory
	site    core.Site
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	site := core.Site{ID: "default", PublicURL: "https://example.test", SitemapURL: "https://example.test/sitemap.xml", Timezone: "UTC"}
	if err := st.EnsureSite(context.Background(), site); err != nil {
		t.Fatal(err)
	}

	reg := registry.New()
	fake := providertest.NewFake(fakeProvider)
	if err := reg.Register(fake); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureConnection(context.Background(), site.ID, fakeProvider); err != nil {
		t.Fatal(err)
	}

	sec := secrets.NewMemory()
	engine := NewEngine(st, reg, sec, site, Config{LookbackDays: 7}, nil)
	return &fixture{engine: engine, store: st, fake: fake, secrets: sec, site: site}
}

func (f *fixture) configure(t *testing.T) core.CredentialRef {
	t.Helper()
	ref, err := f.secrets.Put(context.Background(), core.Scope{SiteID: f.site.ID, Provider: fakeProvider}, core.SecretMaterial{Type: "api_key", Bytes: []byte("k")})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConfigureConnection(context.Background(), f.site.ID, fakeProvider, ref, "fake-property", true); err != nil {
		t.Fatal(err)
	}
	return ref
}

func waitStatus(t *testing.T, st *store.Store, id string, want ...core.SyncRunStatus) core.SyncRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := st.GetSyncRun(context.Background(), "default", id)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range want {
			if run.Status == status {
				return run
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s never reached %v", id, want)
	return core.SyncRun{}
}

func TestCreateRequiresConfiguredConnection(t *testing.T) {
	f := newFixture(t)
	_, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance})
	if se, ok := err.(*core.SyncError); !ok || se.Code != core.ErrNotConfigured {
		t.Fatalf("want NOT_CONFIGURED, got %v", err)
	}
}

func TestCreateRejectsUnsupportedCapability(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	_, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapConversion})
	if se, ok := err.(*core.SyncError); !ok || se.Code != core.ErrUnsupported {
		t.Fatalf("want UNSUPPORTED, got %v", err)
	}
}

func TestSyncSucceedsAndUpdatesConnection(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	through := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	f.fake.SyncFunc = func(ctx context.Context, req core.SyncRequest, _ core.CredentialHandle, sink core.SnapshotSink) (core.SyncResult, error) {
		if err := sink.Write(ctx, "search_daily", []map[string]any{{"_key": "2026-08-15", "clicks": 3}}); err != nil {
			return core.SyncResult{}, err
		}
		return core.SyncResult{Rows: 1, DataThroughDate: through}, nil
	}

	result, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance, TriggeredBy: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	run := waitStatus(t, f.store, result.Run.ID, core.SyncSucceeded)
	if run.RowsSynced != 1 {
		t.Errorf("rows synced = %d, want 1", run.RowsSynced)
	}
	connection, err := f.store.GetConnection(context.Background(), f.site.ID, fakeProvider)
	if err != nil {
		t.Fatal(err)
	}
	if connection.LastSuccessAt == nil {
		t.Error("last success not recorded")
	}
	if connection.DataThroughDate == nil || !connection.DataThroughDate.Equal(through) {
		t.Errorf("data through = %v, want %v", connection.DataThroughDate, through)
	}
}

func TestSyncClassifiedFailure(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	f.fake.SyncFunc = func(context.Context, core.SyncRequest, core.CredentialHandle, core.SnapshotSink) (core.SyncResult, error) {
		return core.SyncResult{}, &core.SyncError{Code: core.ErrUnauthorized, Message: "token expired"}
	}
	result, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance})
	if err != nil {
		t.Fatal(err)
	}
	run := waitStatus(t, f.store, result.Run.ID, core.SyncFailed)
	if run.ErrorCode != core.ErrUnauthorized {
		t.Errorf("error code = %s, want UNAUTHORIZED", run.ErrorCode)
	}
	connection, _ := f.store.GetConnection(context.Background(), f.site.ID, fakeProvider)
	if connection.LastErrorCode != core.ErrUnauthorized {
		t.Errorf("connection error code = %s, want UNAUTHORIZED", connection.LastErrorCode)
	}
}

func TestUnclassifiedErrorCollapsesToInternal(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	f.fake.SyncFunc = func(context.Context, core.SyncRequest, core.CredentialHandle, core.SnapshotSink) (core.SyncResult, error) {
		return core.SyncResult{}, context.Canceled // any unclassified error
	}
	result, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance})
	if err != nil {
		t.Fatal(err)
	}
	run := waitStatus(t, f.store, result.Run.ID, core.SyncFailed)
	if run.ErrorCode != core.ErrInternal || run.ErrorMessage != "sync failed" {
		t.Errorf("got (%s, %q), want (INTERNAL, \"sync failed\") — raw errors must not leak", run.ErrorCode, run.ErrorMessage)
	}
}

func TestPartialResultKeepsCursorAndSuccessTime(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	f.fake.SyncFunc = func(ctx context.Context, _ core.SyncRequest, _ core.CredentialHandle, sink core.SnapshotSink) (core.SyncResult, error) {
		if err := sink.Checkpoint(ctx, "page-3"); err != nil {
			return core.SyncResult{}, err
		}
		return core.SyncResult{Rows: 40, Cursor: "page-3"}, &core.SyncError{Code: core.ErrPartial, Message: "quota hit after page 3"}
	}
	result, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance})
	if err != nil {
		t.Fatal(err)
	}
	run := waitStatus(t, f.store, result.Run.ID, core.SyncPartial)
	if run.Cursor != "page-3" {
		t.Errorf("cursor = %q, want page-3", run.Cursor)
	}
	connection, _ := f.store.GetConnection(context.Background(), f.site.ID, fakeProvider)
	if connection.LastSuccessAt == nil {
		t.Error("partial progress should still record a success time")
	}
}

func TestNextRunResumesPartialCursor(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	seen := make(chan string, 2)
	f.fake.SyncFunc = func(_ context.Context, request core.SyncRequest, _ core.CredentialHandle, _ core.SnapshotSink) (core.SyncResult, error) {
		seen <- request.Cursor
		if request.Cursor == "" {
			return core.SyncResult{Rows: 20, Cursor: "page-2"}, &core.SyncError{Code: core.ErrPartial, Message: "more rows remain"}
		}
		return core.SyncResult{Rows: 5}, nil
	}
	first, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, f.store, first.Run.ID, core.SyncPartial)
	second, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, f.store, second.Run.ID, core.SyncSucceeded)
	if firstCursor, secondCursor := <-seen, <-seen; firstCursor != "" || secondCursor != "page-2" {
		t.Fatalf("cursors = %q, %q", firstCursor, secondCursor)
	}
}

func TestIdempotencyKeyReturnsExistingRun(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	block := make(chan struct{})
	f.fake.SyncFunc = func(context.Context, core.SyncRequest, core.CredentialHandle, core.SnapshotSink) (core.SyncResult, error) {
		<-block
		return core.SyncResult{}, nil
	}
	first, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance, IdempotencyKey: "retry-me"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance, IdempotencyKey: "retry-me"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.ID != first.Run.ID || !second.AlreadyRunning {
		t.Errorf("retry created a new run: first=%s second=%s already=%v", first.Run.ID, second.Run.ID, second.AlreadyRunning)
	}
	close(block)
	waitStatus(t, f.store, first.Run.ID, core.SyncSucceeded)
}

func TestSingleFlightBlocksConcurrentRuns(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	block := make(chan struct{})
	f.fake.SyncFunc = func(context.Context, core.SyncRequest, core.CredentialHandle, core.SnapshotSink) (core.SyncResult, error) {
		<-block
		return core.SyncResult{}, nil
	}
	first, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance})
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyRunning || second.Run.ID != first.Run.ID {
		t.Errorf("second concurrent create should return the in-flight run")
	}
	close(block)
	run := waitStatus(t, f.store, first.Run.ID, core.SyncSucceeded)
	if calls := f.fake.Calls(); len(calls) != 1 {
		t.Errorf("provider called %d times, want 1", len(calls))
	}
	_ = run
}

func TestSchedulerTickSkipsUnconfigured(t *testing.T) {
	f := newFixture(t)
	f.engine.tick(context.Background())
	if calls := f.fake.Calls(); len(calls) != 0 {
		t.Errorf("unconfigured provider was synced %d times", len(calls))
	}
}

func TestSchedulerTickRunsConfigured(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	f.engine.tick(context.Background())
	runs, err := f.store.ListSyncRuns(context.Background(), "default", fakeProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("scheduler created %d runs, want 1", len(runs))
	}
	waitStatus(t, f.store, runs[0].ID, core.SyncSucceeded)
}

func TestRangeValidation(t *testing.T) {
	f := newFixture(t)
	f.configure(t)
	for _, tc := range []struct{ name, start, end string }{
		{"bad start", "not-a-date", ""},
		{"bad end", "", "2026/08/01"},
		{"inverted", "2026-08-10", "2026-08-01"},
		{"future", "", "2999-01-01"},
	} {
		_, err := f.engine.Create(context.Background(), CreateRequest{Provider: fakeProvider, Capability: core.CapSearchPerformance, StartDate: tc.start, EndDate: tc.end})
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}
