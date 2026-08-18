// Command seo-platform is the standalone single-site server: SQLite storage,
// encrypted local secret store, versioned HTTP API, and the sync scheduler.
// Configuration is environment-only so containers pass everything explicitly.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/auth"
	"github.com/HaticeStudio/seo-platform/internal/httpapi"
	"github.com/HaticeStudio/seo-platform/internal/registry"
	"github.com/HaticeStudio/seo-platform/internal/secrets"
	"github.com/HaticeStudio/seo-platform/internal/store"
	syncengine "github.com/HaticeStudio/seo-platform/internal/sync"
	"github.com/HaticeStudio/seo-platform/providers/bing"
	"github.com/HaticeStudio/seo-platform/providers/searchconsole"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	publicURL := strings.TrimSpace(os.Getenv("SEO_PUBLIC_URL"))
	if publicURL == "" {
		return errors.New("SEO_PUBLIC_URL is required (the site's public base URL)")
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("SEO_PUBLIC_URL must be an absolute URL")
	}
	sitemapURL := strings.TrimSpace(os.Getenv("SEO_SITEMAP_URL"))
	if sitemapURL == "" {
		sitemapURL = strings.TrimRight(publicURL, "/") + "/sitemap.xml"
	}
	site := core.Site{ID: "default", PublicURL: publicURL, SitemapURL: sitemapURL, Timezone: envOr("SEO_TIMEZONE", "UTC")}

	listen := envOr("SEO_LISTEN", "127.0.0.1:8080")

	st, err := store.Open(envOr("SEO_DB_PATH", "data/seo.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.EnsureSite(ctx, site); err != nil {
		return fmt.Errorf("ensure site: %w", err)
	}

	var secretStore core.SecretStore
	if key := strings.TrimSpace(os.Getenv("SEO_SECRETS_MASTER_KEY")); key != "" {
		secretStore, err = secrets.NewFile(envOr("SEO_SECRETS_DIR", "data/secrets"), key)
		if err != nil {
			return fmt.Errorf("open secret store: %w", err)
		}
	} else {
		logger.Warn("SEO_SECRETS_MASTER_KEY is not set; using in-memory secret store (credentials are lost on restart)")
		secretStore = secrets.NewMemory()
	}

	var authenticator auth.Authenticator
	switch {
	case strings.TrimSpace(os.Getenv("SEO_API_KEYS")) != "":
		authenticator, err = auth.NewAPIKey(os.Getenv("SEO_API_KEYS"))
		if err != nil {
			return fmt.Errorf("parse SEO_API_KEYS: %w", err)
		}
	case os.Getenv("SEO_DEV_AUTH") == "true":
		// Development auth must never face a network: refuse non-loopback binds.
		host, _, splitErr := net.SplitHostPort(listen)
		if splitErr != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("SEO_DEV_AUTH=true requires SEO_LISTEN on a loopback address")
		}
		authenticator = auth.DevLoopback{}
	default:
		return errors.New("no auth configured: set SEO_API_KEYS, or SEO_DEV_AUTH=true for loopback development")
	}

	// The standalone binary ships every provider; each stays not_configured
	// (and costs nothing) until an administrator adds credentials.
	reg := registry.New()
	for _, provider := range []core.Provider{bing.New(), searchconsole.New()} {
		if err := reg.Register(provider); err != nil {
			return err
		}
	}

	for _, d := range reg.Descriptors() {
		if err := st.EnsureConnection(ctx, site.ID, d.Name); err != nil {
			return fmt.Errorf("ensure connection %s: %w", d.Name, err)
		}
	}

	engine := syncengine.NewEngine(st, reg, secretStore, site, syncengine.Config{
		LookbackDays: envInt("SEO_SYNC_LOOKBACK_DAYS", 30),
		Timeout:      envDuration("SEO_SYNC_TIMEOUT", 10*time.Minute),
		Interval:     envDuration("SEO_SYNC_INTERVAL", 24*time.Hour),
	}, logger)

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go engine.Run(runCtx)

	api := httpapi.New(st, reg, engine, authenticator, site, logger)
	server := &http.Server{Addr: listen, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-runCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("seo-platform listening", "addr", listen, "site", site.PublicURL)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return fallback
}
