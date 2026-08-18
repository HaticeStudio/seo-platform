package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/registry"
	"github.com/HaticeStudio/seo-platform/internal/secrets"
	"github.com/HaticeStudio/seo-platform/internal/store"
	syncengine "github.com/HaticeStudio/seo-platform/internal/sync"
	"github.com/HaticeStudio/seo-platform/providertest"
)

type staticAuth struct{ subject core.Subject }

func (a staticAuth) Authenticate(*http.Request) (core.Subject, error) {
	if a.subject.ID == "" {
		return core.Subject{}, http.ErrNoCookie
	}
	return a.subject, nil
}

func newTestServer(t *testing.T, subject core.Subject) (*Server, *store.Store, *secrets.Memory, core.Site) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	site := core.Site{ID: "default", PublicURL: "https://example.test"}
	if err := st.EnsureSite(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := reg.Register(providertest.NewFake("fake-search")); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureConnection(context.Background(), site.ID, "fake-search"); err != nil {
		t.Fatal(err)
	}
	sec := secrets.NewMemory()
	engine := syncengine.NewEngine(st, reg, sec, site, syncengine.Config{}, nil)
	return New(st, reg, engine, staticAuth{subject}, sec, site, nil), st, sec, site
}

func TestUnauthenticatedRequestsRejected(t *testing.T) {
	server, _, _, _ := newTestServer(t, core.Subject{})
	for _, path := range []string{"/api/v0/providers", "/api/v0/connections", "/api/v0/sync-runs"} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", path, rec.Code)
		}
	}
}

func TestScopeEnforced(t *testing.T) {
	server, _, _, _ := newTestServer(t, core.Subject{ID: "u", Scopes: []string{core.ScopeRead}})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/v0/sync-runs", strings.NewReader(`{}`)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("sync without scope: status %d, want 403", rec.Code)
	}
}

func TestReportRowsAreReadableAndSiteScoped(t *testing.T) {
	server, st, _, site := newTestServer(t, core.Subject{ID: "u", Scopes: []string{core.ScopeRead}})
	ctx := context.Background()
	if err := st.UpsertReportRows(ctx, site.ID, "search/daily", []map[string]any{{"_key": "2026-08-18", "clicks": 4}}); err != nil {
		t.Fatal(err)
	}
	other := core.Site{ID: "other", PublicURL: "https://other.example.test"}
	if err := st.EnsureSite(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertReportRows(ctx, other.ID, "search/daily", []map[string]any{{"_key": "2026-08-18", "clicks": 99}}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, server, "GET", "/api/v0/report-rows?dataset=search%2Fdaily", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("report rows: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"clicks":4`) || strings.Contains(rec.Body.String(), `"clicks":99`) {
		t.Fatalf("site scope leaked: %s", rec.Body.String())
	}
}

func TestSyncRunsAndIdempotencyAreSiteScoped(t *testing.T) {
	server, st, _, site := newTestServer(t, core.Subject{ID: "u", Scopes: []string{core.ScopeRead}})
	ctx := context.Background()
	other := core.Site{ID: "other", PublicURL: "https://other.example.test"}
	if err := st.EnsureSite(ctx, other); err != nil {
		t.Fatal(err)
	}
	base := core.SyncRun{
		Provider: "fake-search", Capability: core.CapSearchPerformance,
		StartDate:      time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		EndDate:        time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		IdempotencyKey: "same-client-key",
	}
	first, inserted, err := st.CreateSyncRun(ctx, base, site.ID)
	if err != nil || !inserted {
		t.Fatalf("create default run: inserted=%v err=%v", inserted, err)
	}
	second, inserted, err := st.CreateSyncRun(ctx, base, other.ID)
	if err != nil || !inserted {
		t.Fatalf("same idempotency key in another site: inserted=%v err=%v", inserted, err)
	}
	rec := do(t, server, "GET", "/api/v0/sync-runs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("sync runs: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), first.ID) || strings.Contains(rec.Body.String(), second.ID) {
		t.Fatalf("site scope leaked: %s", rec.Body.String())
	}
}

func TestConnectionResponseNeverContainsSecret(t *testing.T) {
	subject := core.Subject{ID: "u", Scopes: []string{core.ScopeRead}}
	server, st, sec, site := newTestServer(t, subject)

	const secretValue = "very-secret-token"
	ref, err := sec.Put(context.Background(), core.Scope{SiteID: site.ID, Provider: "fake-search"}, core.SecretMaterial{Type: "api_key", Bytes: []byte(secretValue)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureConnection(context.Background(), site.ID, "fake-search", ref, "prop", true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v0/connections", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, secretValue) {
		t.Fatal("secret material leaked into connection response")
	}
	if strings.Contains(body, ref.ID) {
		t.Fatal("credential ref ID leaked; API should only say configured=true")
	}
	var parsed struct {
		Connections []struct {
			Provider   string `json:"provider"`
			Configured bool   `json:"configured"`
			State      string `json:"state"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Connections) != 1 || !parsed.Connections[0].Configured {
		t.Fatalf("unexpected connections payload: %s", body)
	}
}

func TestProvidersEndpointExposesDescriptors(t *testing.T) {
	server, _, _, _ := newTestServer(t, core.Subject{ID: "u", Scopes: []string{core.ScopeRead}})
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v0/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "search.performance") {
		t.Errorf("capabilities missing from response: %s", rec.Body.String())
	}
}

func TestSiteEndpointExposesOnlyNonSecretSetupValues(t *testing.T) {
	server, _, _, _ := newTestServer(t, core.Subject{ID: "u", Scopes: []string{core.ScopeRead}})
	server.WithPlatformURL("https://seo.example.test")
	rec := do(t, server, "GET", "/api/v0/site", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, value := range []string{"https://example.test", "https://seo.example.test/oauth/callback"} {
		if !strings.Contains(rec.Body.String(), value) {
			t.Errorf("setup value %q missing: %s", value, rec.Body.String())
		}
	}
	for _, forbidden := range []string{"credential", "secret", "token"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), forbidden) {
			t.Errorf("site response contains secret-shaped field %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestHealthEndpointsNeedNoAuth(t *testing.T) {
	server, _, _, _ := newTestServer(t, core.Subject{})
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", path, rec.Code)
		}
	}
}
