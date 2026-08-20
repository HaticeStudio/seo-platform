package platform_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/secrets"
	"github.com/HaticeStudio/seo-platform/platform"
	"github.com/HaticeStudio/seo-platform/providertest"
)

func TestRuntimeMountsInsideHostWithoutPlatformAPIKey(t *testing.T) {
	site := core.Site{
		ID:         "host-site",
		PublicURL:  "https://example.test",
		SitemapURL: "https://example.test/sitemap.xml",
		Timezone:   "Asia/Taipei",
	}
	provider := providertest.NewFake("fake-search")
	provider.Desc.CredentialTypes = append(provider.Desc.CredentialTypes, "oauth2")
	runtime, err := platform.New(context.Background(), platform.Config{
		Site:      site,
		StorePath: filepath.Join(t.TempDir(), "seo.db"),
		Secrets:   secrets.NewMemory(),
		Authenticator: platform.AuthenticateFunc(func(r *http.Request) (core.Subject, error) {
			if r.Header.Get("Cookie") != "host_session=valid" {
				return core.Subject{}, context.Canceled
			}
			return core.Subject{ID: "host-admin", Scopes: []string{core.ScopeRead, core.ScopeConnectionsManage}}, nil
		}),
		Providers: []core.Provider{provider},
		OAuthApps: map[string]platform.OAuthApp{
			"fake-search": {
				ClientID: "client", ClientSecret: "secret",
				AuthURL:  "https://accounts.example.test/authorize",
				TokenURL: "https://accounts.example.test/token",
				Scopes:   []string{"read"},
			},
		},
		OAuthCallbackURL: "https://example.test/admin/seo/oauth/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	host := http.NewServeMux()
	host.Handle("/admin/seo/", http.StripPrefix("/admin/seo", runtime.Handler()))
	request := httptest.NewRequest(http.MethodGet, "/admin/seo/api/v0/site", nil)
	request.Header.Set("Cookie", "host_session=valid")
	response := httptest.NewRecorder()
	host.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["oauth_callback"] != "https://example.test/admin/seo/oauth/callback" {
		t.Fatalf("oauth_callback = %q", body["oauth_callback"])
	}

	oauthRequest := httptest.NewRequest(http.MethodPost, "/admin/seo/api/v0/connections/fake-search/oauth/start", strings.NewReader(`{
		"redirect_uri":"https://example.test/admin/seo/oauth/callback",
		"return_to":"/admin/seo"
	}`))
	oauthRequest.Header.Set("Cookie", "host_session=valid")
	oauthResponse := httptest.NewRecorder()
	host.ServeHTTP(oauthResponse, oauthRequest)
	if oauthResponse.Code != http.StatusOK {
		t.Fatalf("oauth status = %d, body = %s", oauthResponse.Code, oauthResponse.Body.String())
	}
}

func TestRuntimeRequiresHostSecurityBoundaries(t *testing.T) {
	base := platform.Config{
		Site: core.Site{
			ID:         "host-site",
			PublicURL:  "https://example.test",
			SitemapURL: "https://example.test/sitemap.xml",
		},
		Providers: []core.Provider{providertest.NewFake("fake-search")},
	}
	if _, err := platform.New(context.Background(), base); err == nil {
		t.Fatal("expected missing secret store error")
	}
	base.Secrets = secrets.NewMemory()
	if _, err := platform.New(context.Background(), base); err == nil {
		t.Fatal("expected missing host authenticator error")
	}
}

func TestRuntimeImportsExistingHostCredentialWithoutBrowserRoundTrip(t *testing.T) {
	provider := providertest.NewFake("fake-search")
	runtime, err := platform.New(context.Background(), platform.Config{
		Site: core.Site{
			ID: "host-site", PublicURL: "https://example.test",
			SitemapURL: "https://example.test/sitemap.xml",
		},
		StorePath: filepath.Join(t.TempDir(), "seo.db"),
		Secrets:   secrets.NewMemory(),
		Authenticator: platform.AuthenticateFunc(func(*http.Request) (core.Subject, error) {
			return core.Subject{ID: "host-admin", Scopes: []string{core.ScopeRead}}, nil
		}),
		Providers: []core.Provider{provider},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	result, err := runtime.ImportConnection(context.Background(), platform.ImportConnectionRequest{
		Provider: "fake-search",
		Credential: core.SecretMaterial{
			Type: "api_key", Bytes: []byte("existing-host-secret"),
		},
		PropertyReference: "fake-property",
		Actor:             "host-migration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Imported || !result.Connection.Enabled || result.Connection.PropertyReference != "fake-property" {
		t.Fatalf("import result = %+v", result)
	}
	if len(result.Properties) != 1 || result.Properties[0].Reference != "fake-property" {
		t.Fatalf("discovered properties = %+v", result.Properties)
	}

	// Startup reconciliation is create-only. A second call must not rotate the
	// secret an administrator may already have changed through the Console.
	second, err := runtime.ImportConnection(context.Background(), platform.ImportConnectionRequest{
		Provider: "fake-search",
		Credential: core.SecretMaterial{
			Type: "api_key", Bytes: []byte("must-not-replace"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Imported || second.Connection.CredentialRef != result.Connection.CredentialRef {
		t.Fatalf("idempotent import = %+v, want existing ref", second)
	}
}

func TestRuntimeImportCanLeavePropertySelectionToConsole(t *testing.T) {
	runtime, err := platform.New(context.Background(), platform.Config{
		Site: core.Site{
			ID: "host-site", PublicURL: "https://example.test",
			SitemapURL: "https://example.test/sitemap.xml",
		},
		StorePath: filepath.Join(t.TempDir(), "seo.db"),
		Secrets:   secrets.NewMemory(),
		Authenticator: platform.AuthenticateFunc(func(*http.Request) (core.Subject, error) {
			return core.Subject{ID: "host-admin", Scopes: []string{core.ScopeRead}}, nil
		}),
		Providers: []core.Provider{providertest.NewFake("fake-search")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	result, err := runtime.ImportConnection(context.Background(), platform.ImportConnectionRequest{
		Provider:   "fake-search",
		Credential: core.SecretMaterial{Type: "api_key", Bytes: []byte("existing-host-secret")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Imported || result.Connection.Enabled || result.Connection.PropertyReference != "" {
		t.Fatalf("import result = %+v", result)
	}
	if len(result.Properties) != 1 {
		t.Fatalf("properties = %+v", result.Properties)
	}
}
