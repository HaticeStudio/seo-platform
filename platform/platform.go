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

// ImportConnectionRequest lets an embedding host move an existing provider
// credential into the module-owned SecretStore without sending that material
// through a browser. It is intentionally create-only: an already configured
// connection is never rotated or overwritten by startup reconciliation.
type ImportConnectionRequest struct {
	Provider          string
	Credential        core.SecretMaterial
	PropertyReference string
	Actor             string
	// RetainOnDiscoveryFailure lets a trusted embedding host stage an existing
	// credential when provider access has not been granted yet. The connection
	// remains disabled and the Console can retry discovery after an
	// administrator fixes provider-side permissions. Browser credential entry
	// remains on the stricter HTTP path and does not use this option.
	RetainOnDiscoveryFailure bool
}

// ImportConnectionResult reports whether this call imported the credential.
// Properties contains the provider-side choices visible to that credential so
// the host can leave final property selection to the embedded Console.
type ImportConnectionResult struct {
	Imported              bool
	Connection            core.ProviderConnection
	Properties            []core.Property
	DiscoveryErrorCode    core.ErrorCode
	DiscoveryErrorMessage string
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
	store    *store.Store
	registry *registry.Registry
	secrets  core.SecretStore
	site     core.Site
	logger   *slog.Logger
	engine   *syncengine.Engine
	handler  http.Handler
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
	return &Runtime{
		store: st, registry: reg, secrets: cfg.Secrets, site: cfg.Site, logger: cfg.Logger,
		engine: engine, handler: api.Handler(),
	}, nil
}

// Handler returns the module's versioned HTTP API. A host commonly mounts it
// with http.StripPrefix("/admin/seo", runtime.Handler()).
func (r *Runtime) Handler() http.Handler { return r.handler }

// Start runs background sync until the host context is cancelled. Call it in
// a goroutine owned by the host application.
func (r *Runtime) Start(ctx context.Context) { r.engine.Run(ctx) }

// Close releases module persistence resources.
func (r *Runtime) Close() error { return r.store.Close() }

// ImportConnection securely imports an existing host credential. The secret
// is validated through provider discovery before its opaque reference is
// persisted. If PropertyReference is empty, the connection remains disabled
// until an administrator selects one from Properties in the Console.
//
// ImportConnection never replaces an existing credential. Hosts may safely
// call it during startup migration; subsequent calls are no-ops.
func (r *Runtime) ImportConnection(ctx context.Context, req ImportConnectionRequest) (ImportConnectionResult, error) {
	providerName := strings.TrimSpace(req.Provider)
	credentialType := strings.TrimSpace(req.Credential.Type)
	if providerName == "" {
		return ImportConnectionResult{}, errors.New("provider is required")
	}
	provider, ok := r.registry.Get(providerName)
	if !ok {
		return ImportConnectionResult{}, fmt.Errorf("provider %q is not installed", providerName)
	}
	if credentialType == "" || len(req.Credential.Bytes) == 0 {
		return ImportConnectionResult{}, errors.New("credential type and material are required")
	}
	if !descriptorAcceptsCredential(provider.Descriptor(), credentialType) {
		return ImportConnectionResult{}, fmt.Errorf("provider %q does not accept credential type %q", providerName, credentialType)
	}

	current, err := r.store.GetConnection(ctx, r.site.ID, providerName)
	if err != nil {
		return ImportConnectionResult{}, fmt.Errorf("get provider connection: %w", err)
	}
	if current.CredentialRef.ID != "" {
		return ImportConnectionResult{Connection: current}, nil
	}

	scope := core.Scope{SiteID: r.site.ID, Provider: providerName}
	ref, err := r.secrets.Put(ctx, scope, core.SecretMaterial{
		Type: credentialType, Bytes: append([]byte(nil), req.Credential.Bytes...),
	})
	if err != nil {
		return ImportConnectionResult{}, fmt.Errorf("store imported credential: %w", err)
	}
	keepSecret := false
	defer func() {
		if !keepSecret {
			_ = r.secrets.Revoke(context.WithoutCancel(ctx), scope, ref)
		}
	}()

	handle, err := r.secrets.Open(ctx, scope, ref, core.PurposeTest)
	if err != nil {
		return ImportConnectionResult{}, errors.New("open imported credential")
	}
	properties, discoverErr := provider.DiscoverProperties(ctx, handle)
	handle.Close()
	if discoverErr != nil {
		if !req.RetainOnDiscoveryFailure || strings.TrimSpace(req.PropertyReference) != "" {
			return ImportConnectionResult{}, fmt.Errorf("discover provider properties: %w", discoverErr)
		}
	}

	propertyReference := strings.TrimSpace(req.PropertyReference)
	if propertyReference != "" {
		handle, err = r.secrets.Open(ctx, scope, ref, core.PurposeTest)
		if err != nil {
			return ImportConnectionResult{}, errors.New("open imported credential")
		}
		testErr := provider.Test(ctx, r.site, core.Property{Reference: propertyReference}, handle)
		handle.Close()
		if testErr != nil {
			return ImportConnectionResult{}, fmt.Errorf("test provider property: %w", testErr)
		}
	}

	swapped, err := r.store.ConfigureConnectionCAS(
		ctx, r.site.ID, providerName, current.CredentialRef, ref,
		propertyReference, propertyReference != "",
	)
	if err != nil {
		return ImportConnectionResult{}, fmt.Errorf("configure imported connection: %w", err)
	}
	if !swapped {
		latest, latestErr := r.store.GetConnection(ctx, r.site.ID, providerName)
		if latestErr != nil {
			return ImportConnectionResult{}, errors.New("provider connection changed during import")
		}
		return ImportConnectionResult{Connection: latest}, nil
	}
	keepSecret = true

	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = "host-import"
	}
	if err := r.store.AppendAudit(context.WithoutCancel(ctx), core.AuditEvent{
		Actor: actor, Action: "connection.credential.import", Target: providerName, Result: "ok",
	}); err != nil {
		// Import success must not be rolled back because an append-only audit
		// write failed. The host will still receive the operational error in logs.
		r.logger.Error("append imported connection audit", "provider", providerName, "error", err)
	}
	connection, err := r.store.GetConnection(ctx, r.site.ID, providerName)
	if err != nil {
		return ImportConnectionResult{}, fmt.Errorf("read imported connection: %w", err)
	}
	result := ImportConnectionResult{Imported: true, Connection: connection, Properties: properties}
	if discoverErr != nil {
		result.DiscoveryErrorCode, result.DiscoveryErrorMessage = safeProviderError(discoverErr)
	}
	return result, nil
}

func safeProviderError(err error) (core.ErrorCode, string) {
	var providerErr *core.SyncError
	if errors.As(err, &providerErr) {
		return providerErr.Code, providerErr.Message
	}
	return core.ErrInternal, "provider property discovery failed"
}

func descriptorAcceptsCredential(descriptor core.Descriptor, credentialType string) bool {
	for _, accepted := range descriptor.CredentialTypes {
		if accepted == credentialType {
			return true
		}
	}
	return false
}

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
