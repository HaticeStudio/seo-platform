// Package httpapi is the versioned public API (v0). Responses never contain
// credential material — connections expose only "configured" plus ref
// metadata, matching ADR 0005.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/auth"
	"github.com/HaticeStudio/seo-platform/internal/registry"
	"github.com/HaticeStudio/seo-platform/internal/store"
	syncengine "github.com/HaticeStudio/seo-platform/internal/sync"
)

type Server struct {
	store            *store.Store
	registry         *registry.Registry
	engine           *syncengine.Engine
	auth             auth.Authenticator
	secrets          core.SecretStore
	site             core.Site
	logger           *slog.Logger
	consoleDir       string
	oauthApps        map[string]OAuthApp
	oauthCallbackURL string
}

func New(st *store.Store, reg *registry.Registry, engine *syncengine.Engine, authn auth.Authenticator, sec core.SecretStore, site core.Site, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		store: st, registry: reg, engine: engine, auth: authn, secrets: sec,
		site: site, logger: logger,
		oauthCallbackURL: strings.TrimRight(site.PublicURL, "/") + "/oauth/callback",
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Ping(r.Context()); err != nil {
			http.Error(w, "store unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /api/v0/providers", s.authorized(core.ScopeRead, s.listProviders))
	mux.Handle("GET /api/v0/site", s.authorized(core.ScopeRead, s.getSite))
	mux.Handle("GET /api/v0/connections", s.authorized(core.ScopeRead, s.listConnections))
	mux.Handle("GET /api/v0/sync-runs", s.authorized(core.ScopeRead, s.listSyncRuns))
	mux.Handle("GET /api/v0/report-datasets", s.authorized(core.ScopeRead, s.listReportDatasets))
	mux.Handle("GET /api/v0/report-rows", s.authorized(core.ScopeRead, s.listReportRows))
	mux.Handle("POST /api/v0/sync-runs", s.authorized(core.ScopeSync, s.createSyncRun))
	mux.Handle("PUT /api/v0/connections/{provider}/credential", s.authorized(core.ScopeConnectionsManage, s.setCredential))
	mux.Handle("PUT /api/v0/connections/{provider}/property", s.authorized(core.ScopeConnectionsManage, s.setProperty))
	mux.Handle("GET /api/v0/connections/{provider}/properties", s.authorized(core.ScopeConnectionsManage, s.listProperties))
	mux.Handle("POST /api/v0/connections/{provider}/test", s.authorized(core.ScopeConnectionsManage, s.testConnection))
	mux.Handle("DELETE /api/v0/connections/{provider}", s.authorized(core.ScopeConnectionsManage, s.revokeConnection))
	mux.Handle("POST /api/v0/connections/{provider}/oauth/start", s.authorized(core.ScopeConnectionsManage, s.oauthStart))
	mux.Handle("POST /api/v0/connections/{provider}/oauth/complete", s.authorized(core.ScopeConnectionsManage, s.oauthComplete))
	if s.consoleDir != "" {
		mux.Handle("GET /", spaHandler(s.consoleDir))
	}
	return mux
}

// WithPlatformURL keeps the standalone configuration contract. Embedded hosts
// should use WithOAuthCallbackURL with their exact existing admin path.
func (s *Server) WithPlatformURL(publicURL string) *Server {
	s.oauthCallbackURL = strings.TrimRight(publicURL, "/") + "/oauth/callback"
	return s
}

// WithOAuthCallbackURL sets the exact public callback URL registered with the
// provider. Embedded hosts normally point this at an existing admin path, for
// example https://example.com/admin/seo/oauth/callback. No separate origin is
// required.
func (s *Server) WithOAuthCallbackURL(callbackURL string) *Server {
	s.oauthCallbackURL = strings.TrimSpace(callbackURL)
	return s
}

// WithConsole serves the built console app from dir. The console is static
// assets only; every data request still passes the auth boundary above.
func (s *Server) WithConsole(dir string) *Server {
	s.consoleDir = dir
	return s
}

// spaHandler serves static files, falling back to index.html for client-side
// routes. Paths are cleaned by http.FileServer; unknown files 404 via the
// fallback probe below rather than leaking directory listings.
func spaHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe := filepath.Join(dir, filepath.FromSlash(path.Clean("/"+r.URL.Path)))
		if info, err := os.Stat(probe); err != nil || info.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, subject core.Subject)

func (s *Server) authorized(scope string, next handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject, err := s.auth.Authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if !subject.HasScope(scope) {
			writeError(w, http.StatusForbidden, "missing scope "+scope)
			return
		}
		next(w, r, subject)
	})
}

// providerJSON is the Console-facing descriptor. Setup URLs come from the
// provider itself so the UI never hardcodes third-party paths.
type providerJSON struct {
	Name            string           `json:"name"`
	DisplayName     string           `json:"display_name"`
	CredentialTypes []string         `json:"credential_types"`
	Capabilities    []capabilityJSON `json:"capabilities"`
	SetupURL        string           `json:"setup_url,omitempty"`
	DocsURL         string           `json:"docs_url,omitempty"`
	OAuthAvailable  bool             `json:"oauth_available"`
	SetupLinks      []setupLinkJSON  `json:"setup_links,omitempty"`
}

type setupLinkJSON struct {
	Kind        string `json:"kind,omitempty"`
	Label       string `json:"label"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

type capabilityJSON struct {
	Capability       string   `json:"capability"`
	Dimensions       []string `json:"dimensions,omitempty"`
	Metrics          []string `json:"metrics,omitempty"`
	MaxRangeDays     int      `json:"max_range_days,omitempty"`
	FreshnessLagDays int      `json:"freshness_lag_days,omitempty"`
	SupportsCursor   bool     `json:"supports_cursor"`
	QuotaHint        string   `json:"quota_hint,omitempty"`
}

func (s *Server) listProviders(w http.ResponseWriter, _ *http.Request, _ core.Subject) {
	descriptors := s.registry.Descriptors()
	out := make([]providerJSON, 0, len(descriptors))
	for _, d := range descriptors {
		_, oauthAvailable := s.oauthApps[d.Name]
		p := providerJSON{Name: d.Name, DisplayName: d.DisplayName, CredentialTypes: d.CredentialTypes, SetupURL: d.SetupURL, DocsURL: d.DocsURL, OAuthAvailable: oauthAvailable}
		for _, link := range d.SetupLinks {
			p.SetupLinks = append(p.SetupLinks, setupLinkJSON{Kind: link.Kind, Label: link.Label, URL: link.URL, Description: link.Description})
		}
		for _, c := range d.Capabilities {
			p.Capabilities = append(p.Capabilities, capabilityJSON{
				Capability: string(c.Capability), Dimensions: c.Dimensions, Metrics: c.Metrics,
				MaxRangeDays: c.MaxRangeDays, FreshnessLagDays: c.FreshnessLagDays,
				SupportsCursor: c.SupportsCursor, QuotaHint: c.QuotaHint,
			})
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (s *Server) getSite(w http.ResponseWriter, _ *http.Request, _ core.Subject) {
	writeJSON(w, http.StatusOK, map[string]string{
		"public_url":     s.site.PublicURL,
		"sitemap_url":    s.site.SitemapURL,
		"oauth_callback": s.oauthCallbackURL,
	})
}

// connectionJSON exposes state only: "configured" plus credential type and
// timestamps — never material, never a masked secret.
type connectionJSON struct {
	Provider          string `json:"provider"`
	Configured        bool   `json:"configured"`
	Enabled           bool   `json:"enabled"`
	CredentialType    string `json:"credential_type,omitempty"`
	PropertyReference string `json:"property_reference,omitempty"`
	State             string `json:"state"`
	LastSuccessAt     string `json:"last_success_at,omitempty"`
	DataThroughDate   string `json:"data_through_date,omitempty"`
	LastErrorCode     string `json:"last_error_code,omitempty"`
	LastErrorMessage  string `json:"last_error_message,omitempty"`
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request, _ core.Subject) {
	rows, err := s.store.ListConnections(r.Context(), s.site.ID)
	if err != nil {
		s.logger.Error("list connections", "error", err)
		writeError(w, http.StatusInternalServerError, "list connections")
		return
	}
	out := make([]connectionJSON, 0, len(rows))
	for _, c := range rows {
		out = append(out, connectionToJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

func connectionToJSON(c core.ProviderConnection) connectionJSON {
	out := connectionJSON{
		Provider:          c.Provider,
		Configured:        c.CredentialRef.ID != "",
		Enabled:           c.Enabled,
		CredentialType:    c.CredentialRef.Type,
		PropertyReference: c.PropertyReference,
		State:             connectionState(c),
		LastErrorCode:     string(c.LastErrorCode),
		LastErrorMessage:  c.LastErrorMessage,
	}
	if c.LastSuccessAt != nil {
		out.LastSuccessAt = c.LastSuccessAt.UTC().Format(time.RFC3339)
	}
	if c.DataThroughDate != nil {
		out.DataThroughDate = c.DataThroughDate.Format("2006-01-02")
	}
	return out
}

// connectionState distinguishes not-configured, error, stale, and connected —
// the UI never guesses from empty arrays (ADR 0005 failure modes).
func connectionState(c core.ProviderConnection) string {
	switch {
	case c.CredentialRef.ID == "":
		return "not_configured"
	case c.LastErrorCode != core.ErrNone:
		if c.LastErrorCode == core.ErrUnauthorized {
			return "reauthorization_required"
		}
		return "error"
	case !c.Enabled || c.PropertyReference == "":
		return "needs_property"
	case c.DataThroughDate != nil && time.Since(*c.DataThroughDate) > 5*24*time.Hour:
		return "stale"
	case c.LastSuccessAt == nil:
		return "no_data"
	default:
		return "connected"
	}
}

type syncRunJSON struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	Capability     string `json:"capability"`
	StartDate      string `json:"start_date"`
	EndDate        string `json:"end_date"`
	Status         string `json:"status"`
	RowsSynced     int64  `json:"rows_synced"`
	Cursor         string `json:"cursor,omitempty"`
	TriggeredBy    string `json:"triggered_by,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
	ErrorCode      string `json:"error_code,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at,omitempty"`
}

func runToJSON(r core.SyncRun) syncRunJSON {
	out := syncRunJSON{
		ID: r.ID, Provider: r.Provider, Capability: string(r.Capability),
		StartDate: r.StartDate.Format("2006-01-02"), EndDate: r.EndDate.Format("2006-01-02"),
		Status: string(r.Status), RowsSynced: r.RowsSynced, Cursor: r.Cursor,
		TriggeredBy: r.TriggeredBy, IdempotencyKey: r.IdempotencyKey,
		ErrorCode: string(r.ErrorCode), ErrorMessage: r.ErrorMessage,
		StartedAt: r.StartedAt.UTC().Format(time.RFC3339),
	}
	if r.FinishedAt != nil {
		out.FinishedAt = r.FinishedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (s *Server) listSyncRuns(w http.ResponseWriter, r *http.Request, _ core.Subject) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.store.ListSyncRuns(r.Context(), s.site.ID, r.URL.Query().Get("provider"), limit)
	if err != nil {
		s.logger.Error("list sync runs", "error", err)
		writeError(w, http.StatusInternalServerError, "list sync runs")
		return
	}
	out := make([]syncRunJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, runToJSON(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sync_runs": out})
}

func (s *Server) listReportDatasets(w http.ResponseWriter, r *http.Request, _ core.Subject) {
	datasets, err := s.store.ListReportDatasets(r.Context(), s.site.ID)
	if err != nil {
		s.logger.Error("list report datasets", "error", err)
		writeError(w, http.StatusInternalServerError, "list report datasets")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"datasets": datasets})
}

func (s *Server) listReportRows(w http.ResponseWriter, r *http.Request, _ core.Subject) {
	dataset := r.URL.Query().Get("dataset")
	if dataset == "" {
		writeError(w, http.StatusBadRequest, "dataset is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	after := r.URL.Query().Get("cursor")
	rows, nextCursor, err := s.store.ListReportRowsPage(r.Context(), s.site.ID, dataset, limit, after)
	if err != nil {
		s.logger.Error("list report rows", "dataset", dataset, "error", err)
		writeError(w, http.StatusInternalServerError, "list report rows")
		return
	}
	type reportRowJSON struct {
		Dataset   string         `json:"dataset"`
		Key       string         `json:"key"`
		Data      map[string]any `json:"data"`
		UpdatedAt string         `json:"updated_at"`
	}
	out := make([]reportRowJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, reportRowJSON{Dataset: row.Dataset, Key: row.Key, Data: row.Data, UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out, "next_cursor": nextCursor})
}

type createSyncRunJSON struct {
	Provider       string `json:"provider"`
	Capability     string `json:"capability"`
	StartDate      string `json:"start_date"`
	EndDate        string `json:"end_date"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) createSyncRun(w http.ResponseWriter, r *http.Request, subject core.Subject) {
	var req createSyncRunJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	result, err := s.engine.Create(r.Context(), syncengine.CreateRequest{
		Provider:       req.Provider,
		Capability:     core.Capability(req.Capability),
		StartDate:      req.StartDate,
		EndDate:        req.EndDate,
		IdempotencyKey: req.IdempotencyKey,
		TriggeredBy:    subject.ID,
	})
	if err != nil {
		var classified *core.SyncError
		if errors.As(err, &classified) {
			status := http.StatusBadRequest
			if classified.Code == core.ErrNotConfigured {
				status = http.StatusConflict
			}
			writeError(w, status, classified.Message)
			return
		}
		s.logger.Error("create sync run", "error", err)
		writeError(w, http.StatusInternalServerError, "create sync run")
		return
	}
	audit := core.AuditEvent{Actor: subject.ID, Action: "sync.create", Target: req.Provider + "/" + req.Capability, Result: string(result.Run.Status)}
	if err := s.store.AppendAudit(context.WithoutCancel(r.Context()), audit); err != nil {
		s.logger.Error("append audit event", "error", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"sync_run": runToJSON(result.Run), "already_running": result.AlreadyRunning})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
