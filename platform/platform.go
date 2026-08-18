// Package platform exposes the embeddable SEO runtime.
//
// A host application imports this package, supplies its existing HTTP
// authentication adapter and secret store, then mounts Runtime.Handler under
// an existing admin path. It does not need to run another service or manage a
// seo-platform API key.
package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/httpapi"
	"github.com/HaticeStudio/seo-platform/internal/registry"
	"github.com/HaticeStudio/seo-platform/internal/store"
	syncengine "github.com/HaticeStudio/seo-platform/internal/sync"
)

// Authenticator maps the host's already-established session or identity to
// seo-platform scopes. Implementations normally inspect the host session
// cookie or request context; no second bearer credential is required.
type Authenticator interface {
	Authenticate(*http.Request) (core.Subject, error)
}

// AuthenticateFunc adapts a function to Authenticator.
type AuthenticateFunc func(*http.Request) (core.Subject, error)

func (f AuthenticateFunc) Authenticate(r *http.Request) (core.Subject, error) {
	return f(r)
}

// OAuthApp configures one provider's OAuth client. ClientSecret is consumed
// only by the Go runtime and is never exposed to the React console.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
}

// SyncConfig controls background synchronization.
type SyncConfig struct {
	LookbackDays int
	Timeout      time.Duration
	Interval     time.Duration
	Lease        time.Duration
}

// Config wires one embedded runtime into a host application.
type Config struct {
	Site core.Site

	// StorePath is the module-owned SQLite file inside the host deployment.
	// It defaults to data/seo.db. A storage adapter interface is planned before
	// v1 for hosts that need the tables in an existing database.
	StorePath string

	Secrets       core.SecretStore
	Authenticator Authenticator
	Providers     []core.Provider
	OAuthApps     map[string]OAuthApp

	// OAuthCallbackURL is the exact existing host path registered with the
	// provider, e.g. https://example.com/admin/seo/oauth/callback.
	OAuthCallbackURL string
	Sync             SyncConfig
	Logger           *slog.Logger
}

// Runtime is an in-process module. Mount Handler in the host router, call
// Start with the host lifecycle context, and Close during host shutdown.
type Runtime struct {
	store   *store.Store
	engine  *syncengine.Engine
	handler http.Handler
}

// New constructs and initializes an embedded runtime.
func New(ctx context.Context, cfg Config) (*Runtime, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.StorePath == "" {
		cfg.StorePath = "data/seo.db"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	st, err := store.Open(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("open SEO store: %w", err)
	}
	closeOnError := func(err error) (*Runtime, error) {
		_ = st.Close()
		return nil, err
	}
	if err := st.EnsureSite(ctx, cfg.Site); err != nil {
		return closeOnError(fmt.Errorf("ensure SEO site: %w", err))
	}

	reg := registry.New()
	for _, provider := range cfg.Providers {
		if provider == nil {
			return closeOnError(errors.New("SEO provider must not be nil"))
		}
		if err := reg.Register(provider); err != nil {
			return closeOnError(err)
		}
	}
	for _, descriptor := range reg.Descriptors() {
		if err := st.EnsureConnection(ctx, cfg.Site.ID, descriptor.Name); err != nil {
			return closeOnError(fmt.Errorf("ensure SEO connection %s: %w", descriptor.Name, err))
		}
	}

	engine := syncengine.NewEngine(st, reg, cfg.Secrets, cfg.Site, syncengine.Config{
		LookbackDays: cfg.Sync.LookbackDays,
		Timeout:      cfg.Sync.Timeout,
		Interval:     cfg.Sync.Interval,
		Lease:        cfg.Sync.Lease,
	}, cfg.Logger)
	api := httpapi.New(st, reg, engine, cfg.Authenticator, cfg.Secrets, cfg.Site, cfg.Logger)
	if cfg.OAuthCallbackURL != "" {
		api = api.WithOAuthCallbackURL(cfg.OAuthCallbackURL)
	}
	if len(cfg.OAuthApps) > 0 {
		apps := make(map[string]httpapi.OAuthApp, len(cfg.OAuthApps))
		for provider, app := range cfg.OAuthApps {
			apps[provider] = httpapi.OAuthApp{
				ClientID: app.ClientID, ClientSecret: app.ClientSecret,
				AuthURL: app.AuthURL, TokenURL: app.TokenURL, Scopes: app.Scopes,
			}
		}
		api = api.WithOAuthApps(apps)
	}
	return &Runtime{store: st, engine: engine, handler: api.Handler()}, nil
}

// Handler returns the module's versioned HTTP API. A host commonly mounts it
// with http.StripPrefix("/admin/seo", runtime.Handler()).
func (r *Runtime) Handler() http.Handler { return r.handler }

// Start runs background sync until the host context is cancelled. Call it in
// a goroutine owned by the host application.
func (r *Runtime) Start(ctx context.Context) { r.engine.Run(ctx) }

// Close releases module persistence resources.
func (r *Runtime) Close() error { return r.store.Close() }

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.Site.ID) == "" {
		return errors.New("SEO site ID is required")
	}
	if err := validateAbsoluteURL("SEO site public URL", cfg.Site.PublicURL); err != nil {
		return err
	}
	if cfg.Site.SitemapURL == "" {
		return errors.New("SEO sitemap URL is required")
	}
	if err := validateAbsoluteURL("SEO sitemap URL", cfg.Site.SitemapURL); err != nil {
		return err
	}
	if cfg.Secrets == nil {
		return errors.New("SEO secret store is required")
	}
	if cfg.Authenticator == nil {
		return errors.New("SEO host authenticator is required")
	}
	if len(cfg.Providers) == 0 {
		return errors.New("at least one SEO provider is required")
	}
	if cfg.OAuthCallbackURL != "" {
		if err := validateAbsoluteURL("SEO OAuth callback URL", cfg.OAuthCallbackURL); err != nil {
			return err
		}
	}
	return nil
}

func validateAbsoluteURL(name, raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%s must be an absolute URL", name)
	}
	return nil
}

var _ Authenticator = AuthenticateFunc(nil)
