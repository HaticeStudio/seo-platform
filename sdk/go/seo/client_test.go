package seo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientAuthenticatesAndDecodesProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/v0/providers" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"name":"bing","display_name":"Bing","oauth_available":false}]}`))
	}))
	defer server.Close()
	client, err := New(server.URL, func(context.Context) (string, error) { return "test-token", nil }, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	providers, err := client.Providers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].Name != "bing" {
		t.Fatalf("providers = %+v", providers)
	}
}

func TestOAuthRoundTripPreservesBoundReturnPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v0/connections/google-search-console/oauth/start":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["return_to"] != "/admin/seo?tab=connections" {
				t.Errorf("return_to = %q", body["return_to"])
			}
			_, _ = w.Write([]byte(`{"authorize_url":"https://accounts.example/authorize","state":"opaque-state"}`))
		case "/api/v0/connections/google-search-console/oauth/complete":
			_, _ = w.Write([]byte(`{"configured":true,"return_to":"/admin/seo?tab=connections","properties":[{"reference":"sc-domain:example.test","display_name":"example.test"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, _ := New(server.URL, func(context.Context) (string, error) { return "token", nil }, server.Client())
	authorizeURL, state, err := client.StartOAuthWithReturn(context.Background(), "google-search-console", "https://seo.example/oauth/callback", "/admin/seo?tab=connections")
	if err != nil || authorizeURL == "" || state != "opaque-state" {
		t.Fatalf("start OAuth: url=%q state=%q err=%v", authorizeURL, state, err)
	}
	properties, returnTo, err := client.CompleteOAuthWithReturn(context.Background(), "google-search-console", state, "temporary-code")
	if err != nil || returnTo != "/admin/seo?tab=connections" || len(properties) != 1 {
		t.Fatalf("complete OAuth: properties=%+v return=%q err=%v", properties, returnTo, err)
	}
}

func TestClientReturnsTypedAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"missing scope read"}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, func(context.Context) (string, error) { return "token", nil }, server.Client())
	_, err := client.Connections(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Message != "missing scope read" {
		t.Fatalf("error = %#v", err)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	if _, err := New("relative", func(context.Context) (string, error) { return "x", nil }, nil); err == nil {
		t.Fatal("relative base URL accepted")
	}
	if _, err := New("https://example.test", nil, nil); err == nil {
		t.Fatal("nil token source accepted")
	}
}

func TestReportRowsPageSendsAndReturnsCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("dataset"); got != "search/details" {
			t.Errorf("dataset = %q", got)
		}
		if got := r.URL.Query().Get("cursor"); got != "next|row" {
			t.Errorf("cursor = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "500" {
			t.Errorf("limit = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rows":[{"dataset":"search/details","key":"a","data":{"clicks":1}}],"next_cursor":"last"}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, func(context.Context) (string, error) { return "token", nil }, server.Client())
	rows, next, err := client.ReportRowsPage(context.Background(), "search/details", 500, "next|row")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "a" || next != "last" {
		t.Fatalf("rows=%#v next=%q", rows, next)
	}
}
