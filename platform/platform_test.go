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
