package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/providertest"
)

func adminSubject() core.Subject {
	return core.Subject{ID: "admin", Scopes: []string{core.ScopeRead, core.ScopeSync, core.ScopeConnectionsManage}}
}

func do(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("{}")
	} else {
		reader = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	return rec
}

func TestCredentialLifecycle(t *testing.T) {
	server, st, _, site := newTestServer(t, adminSubject())

	// Set an API key credential; discovery from the fake provider succeeds.
	rec := do(t, server, "PUT", "/api/v0/connections/fake-search/credential",
		`{"credential_type":"api_key","material":"the-secret-key"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set credential: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "the-secret-key") {
		t.Fatal("credential material echoed back")
	}
	if !strings.Contains(rec.Body.String(), "fake-property") {
		t.Errorf("expected discovered properties, got %s", rec.Body.String())
	}

	// Wrong credential type is rejected before touching the secret store.
	rec = do(t, server, "PUT", "/api/v0/connections/fake-search/credential",
		`{"credential_type":"oauth2","material":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("wrong credential type: %d", rec.Code)
	}

	// Choose a property → connection becomes enabled.
	rec = do(t, server, "PUT", "/api/v0/connections/fake-search/property",
		`{"property_reference":"fake-property"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set property: %d %s", rec.Code, rec.Body.String())
	}
	connection, err := st.GetConnection(context.Background(), site.ID, "fake-search")
	if err != nil || !connection.Enabled || connection.PropertyReference != "fake-property" {
		t.Fatalf("connection after property: %+v err=%v", connection, err)
	}

	// Test endpoint reports ok.
	rec = do(t, server, "POST", "/api/v0/connections/fake-search/test", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("test: %d %s", rec.Code, rec.Body.String())
	}

	// Revoke → back to unconfigured, credential unusable.
	previousRef := connection.CredentialRef
	rec = do(t, server, "DELETE", "/api/v0/connections/fake-search", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	connection, _ = st.GetConnection(context.Background(), site.ID, "fake-search")
	if connection.Enabled || connection.CredentialRef.ID != "" {
		t.Errorf("connection still configured after revoke: %+v", connection)
	}
	scope := core.Scope{SiteID: site.ID, Provider: "fake-search"}
	if _, err := server.secrets.Open(context.Background(), scope, previousRef, core.PurposeSync); err == nil {
		t.Error("revoked credential still opens")
	}
}

func TestInvalidCredentialRotationKeepsPreviousCredential(t *testing.T) {
	server, st, _, site := newTestServer(t, adminSubject())
	if rec := do(t, server, "PUT", "/api/v0/connections/fake-search/credential",
		`{"credential_type":"api_key","material":"known-good"}`); rec.Code != http.StatusOK {
		t.Fatalf("initial credential: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, server, "PUT", "/api/v0/connections/fake-search/property",
		`{"property_reference":"fake-property"}`); rec.Code != http.StatusOK {
		t.Fatalf("initial property: %d %s", rec.Code, rec.Body.String())
	}
	before, err := st.GetConnection(context.Background(), site.ID, "fake-search")
	if err != nil {
		t.Fatal(err)
	}
	registered, _ := server.registry.Get("fake-search")
	fake := registered.(*providertest.Fake)
	fake.TestFunc = func(context.Context) error {
		return &core.SyncError{Code: core.ErrUnauthorized, Message: "replacement rejected"}
	}
	rec := do(t, server, "PUT", "/api/v0/connections/fake-search/credential",
		`{"credential_type":"api_key","material":"known-bad"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("invalid replacement: %d %s", rec.Code, rec.Body.String())
	}
	after, err := st.GetConnection(context.Background(), site.ID, "fake-search")
	if err != nil {
		t.Fatal(err)
	}
	if after.CredentialRef != before.CredentialRef || !after.Enabled {
		t.Fatalf("previous credential was replaced or disabled: before=%+v after=%+v", before, after)
	}
	handle, err := server.secrets.Open(context.Background(), core.Scope{SiteID: site.ID, Provider: "fake-search"}, before.CredentialRef, core.PurposeTest)
	if err != nil {
		t.Fatalf("previous credential was revoked: %v", err)
	}
	handle.Close()
}

func TestConnectionEndpointsRequireManageScope(t *testing.T) {
	server, _, _, _ := newTestServer(t, core.Subject{ID: "reader", Scopes: []string{core.ScopeRead}})
	for _, tc := range [][2]string{
		{"PUT", "/api/v0/connections/fake-search/credential"},
		{"PUT", "/api/v0/connections/fake-search/property"},
		{"POST", "/api/v0/connections/fake-search/test"},
		{"DELETE", "/api/v0/connections/fake-search"},
		{"POST", "/api/v0/connections/fake-search/oauth/start"},
	} {
		if rec := do(t, server, tc[0], tc[1], ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", tc[0], tc[1], rec.Code)
		}
	}
}

func TestOAuthFlow(t *testing.T) {
	server, st, _, site := newTestServer(t, adminSubject())

	// Fake authorization server: token endpoint returns a refresh token.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("code") != "good-code" || r.FormValue("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(tokenServer.Close)

	server.WithOAuthApps(map[string]OAuthApp{"fake-search": {
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		AuthURL:      "https://auth.example.test/authorize",
		TokenURL:     tokenServer.URL,
		Scopes:       []string{"read"},
	}})

	rec := do(t, server, "POST", "/api/v0/connections/fake-search/oauth/start",
		`{"redirect_uri":"https://example.test/oauth/callback"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("oauth start: %d %s", rec.Code, rec.Body.String())
	}
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
		State        string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"code_challenge=", "code_challenge_method=S256", "state=" + started.State} {
		if !strings.Contains(started.AuthorizeURL, fragment) {
			t.Errorf("authorize URL missing %s: %s", fragment, started.AuthorizeURL)
		}
	}

	rec = do(t, server, "POST", "/api/v0/connections/fake-search/oauth/complete",
		`{"state":"`+started.State+`","code":"good-code"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("oauth complete: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "rt") && strings.Contains(rec.Body.String(), "refresh") {
		t.Fatal("refresh token leaked into response")
	}

	connection, err := st.GetConnection(context.Background(), site.ID, "fake-search")
	if err != nil || connection.CredentialRef.ID == "" || connection.CredentialRef.Type != "oauth2" {
		t.Fatalf("connection after oauth: %+v err=%v", connection, err)
	}
	scope := core.Scope{SiteID: site.ID, Provider: "fake-search"}
	handle, err := server.secrets.Open(context.Background(), scope, connection.CredentialRef, core.PurposeSync)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	material, err := core.ParseOAuthMaterial(handle.Material().Bytes)
	if err != nil || material.RefreshToken != "rt" {
		t.Fatalf("stored oauth material: %+v err=%v", material, err)
	}

	// State is single use.
	rec = do(t, server, "POST", "/api/v0/connections/fake-search/oauth/complete",
		`{"state":"`+started.State+`","code":"good-code"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("state replay: %d, want 400", rec.Code)
	}
}

func TestOAuthStateIsBoundToStartingSubject(t *testing.T) {
	server, st, _, site := newTestServer(t, adminSubject())
	server.WithOAuthApps(map[string]OAuthApp{"fake-search": {
		ClientID: "client-id", AuthURL: "https://auth.example.test/authorize", TokenURL: "https://auth.example.test/token",
	}})
	rec := do(t, server, "POST", "/api/v0/connections/fake-search/oauth/start",
		`{"redirect_uri":"https://example.test/oauth/callback"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("oauth start: %d %s", rec.Code, rec.Body.String())
	}
	var started struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	server.auth = staticAuth{subject: core.Subject{ID: "other-admin", Scopes: []string{core.ScopeConnectionsManage}}}
	rec = do(t, server, "POST", "/api/v0/connections/fake-search/oauth/complete",
		`{"state":"`+started.State+`","code":"stolen-code"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("another subject consumed OAuth state: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := st.ConsumeOAuthState(context.Background(), started.State, "fake-search", site.ID, "admin"); err != nil {
		t.Fatalf("mismatched subject invalidated the legitimate state: %v", err)
	}
}

func TestOAuthStartRejectsInsecureRedirect(t *testing.T) {
	server, _, _, _ := newTestServer(t, adminSubject())
	server.WithOAuthApps(map[string]OAuthApp{"fake-search": {ClientID: "c", TokenURL: "https://t", AuthURL: "https://a"}})
	rec := do(t, server, "POST", "/api/v0/connections/fake-search/oauth/start",
		`{"redirect_uri":"http://evil.example.test/callback"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("insecure redirect accepted: %d", rec.Code)
	}
}

func TestOAuthUsesConfiguredPlatformOrigin(t *testing.T) {
	server, _, _, _ := newTestServer(t, adminSubject())
	server.WithPlatformURL("https://seo.example.test")
	server.WithOAuthApps(map[string]OAuthApp{"fake-search": {ClientID: "c", TokenURL: "https://t", AuthURL: "https://a"}})
	accepted := do(t, server, "POST", "/api/v0/connections/fake-search/oauth/start",
		`{"redirect_uri":"https://seo.example.test/oauth/callback"}`)
	if accepted.Code != http.StatusOK {
		t.Fatalf("configured platform callback rejected: %d %s", accepted.Code, accepted.Body.String())
	}
	rejected := do(t, server, "POST", "/api/v0/connections/fake-search/oauth/start",
		`{"redirect_uri":"https://example.test/oauth/callback"}`)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("analyzed-site callback accepted instead of platform origin: %d", rejected.Code)
	}
}
