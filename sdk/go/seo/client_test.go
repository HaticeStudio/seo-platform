package seo

import (
	"context"
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
