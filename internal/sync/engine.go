// Package sync orchestrates provider sync runs: validation, single-flight,
// idempotency, timeouts, error classification, and the daily scheduler.
// Providers only pull data and write snapshots; everything operational
// lives here.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/registry"
	"github.com/HaticeStudio/seo-platform/internal/store"
	"github.com/google/uuid"
)

type Config struct {
	// LookbackDays is how far back a default-range sync reaches.
	LookbackDays int
	// Timeout bounds one sync run end to end.
	Timeout time.Duration
	// Interval is the scheduler tick.
	Interval time.Duration
	// Lease is how long a RUNNING run may go without finishing before it is
	// treated as crashed and requeued.
	Lease time.Duration
}

func (c Config) withDefaults() Config {
	if c.LookbackDays <= 0 {
		c.LookbackDays = 30
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Minute
	}
	if c.Interval <= 0 {
		c.Interval = 24 * time.Hour
	}
	if c.Lease <= 0 {
		c.Lease = 30 * time.Minute
	}
	return c
}

type Engine struct {
	store    *store.Store
	registry *registry.Registry
	secrets  core.SecretStore
	site     core.Site
	cfg      Config
	logger   *slog.Logger
	// now is swappable for tests.
	now func() time.Time
}

func NewEngine(st *store.Store, reg *registry.Registry, sec core.SecretStore, site core.Site, cfg Config, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{store: st, registry: reg, secrets: sec, site: site, cfg: cfg.withDefaults(), logger: logger, now: time.Now}
}

// CreateRequest is an API-facing ask for a sync run.
type CreateRequest struct {
	Provider       string
	Capability     core.Capability
	StartDate      string // YYYY-MM-DD, optional
	EndDate        string // YYYY-MM-DD, optional
	IdempotencyKey string
	TriggeredBy    string
}

// CreateResult reports the run plus whether an equivalent run already existed.
type CreateResult struct {
	Run            core.SyncRun
	AlreadyRunning bool
}

// Create validates, inserts, and (when insertion won) launches the run
// asynchronously. Callers watch progress via ListSyncRuns.
func (e *Engine) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	provider, ok := e.registry.Get(req.Provider)
	if !ok {
		return CreateResult{}, &core.SyncError{Code: core.ErrNotConfigured, Message: fmt.Sprintf("provider %q is not installed", req.Provider)}
	}
	descriptor := provider.Descriptor()
	spec, ok := descriptor.Spec(req.Capability)
	if !ok {
		return CreateResult{}, &core.SyncError{Code: core.ErrUnsupported, Message: fmt.Sprintf("provider %q does not support %q", req.Provider, req.Capability)}
	}

	connection, err := e.store.GetConnection(ctx, e.site.ID, req.Provider)
	if err != nil {
		return CreateResult{}, &core.SyncError{Code: core.ErrNotConfigured, Message: "provider connection does not exist"}
	}
	if !connection.Enabled || connection.CredentialRef.ID == "" {
		return CreateResult{}, &core.SyncError{Code: core.ErrNotConfigured, Message: "provider is not configured"}
	}

	start, end, err := e.resolveRange(req.StartDate, req.EndDate, spec)
	if err != nil {
		return CreateResult{}, err
	}

	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		// Explicit keys make client retries idempotent; a fresh request gets a
		// fresh key so completed ranges can be intentionally re-synced.
		key = uuid.NewString()
	}
	if len(key) > 200 {
		return CreateResult{}, &core.SyncError{Code: core.ErrInternal, Message: "idempotency_key is too long"}
	}
	resumeCursor, err := e.store.ResumeCursor(ctx, e.site.ID, req.Provider, string(req.Capability))
	if err != nil {
		return CreateResult{}, fmt.Errorf("read sync checkpoint: %w", err)
	}

	run := core.SyncRun{
		Provider:       req.Provider,
		Capability:     req.Capability,
		StartDate:      start,
		EndDate:        end,
		TriggeredBy:    req.TriggeredBy,
		IdempotencyKey: key,
		Cursor:         resumeCursor,
	}
	created, inserted, err := e.store.CreateSyncRun(ctx, run, e.site.ID)
	if err != nil {
		return CreateResult{}, err
	}
	if !inserted {
		active := created.Status == core.SyncQueued || created.Status == core.SyncRunning
		return CreateResult{Run: created, AlreadyRunning: active}, nil
	}
	go e.execute(created.ID)
	return CreateResult{Run: created}, nil
}

func (e *Engine) resolveRange(startRaw, endRaw string, spec core.CapabilitySpec) (time.Time, time.Time, error) {
	location := time.UTC
	now := e.now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	end := today.AddDate(0, 0, -max(spec.FreshnessLagDays, 1))
	var err error
	if strings.TrimSpace(endRaw) != "" {
		end, err = time.ParseInLocation("2006-01-02", endRaw, location)
		if err != nil {
			return time.Time{}, time.Time{}, &core.SyncError{Code: core.ErrInternal, Message: "end_date must be YYYY-MM-DD"}
		}
	}
	start := end.AddDate(0, 0, -(e.cfg.LookbackDays - 1))
	if strings.TrimSpace(startRaw) != "" {
		start, err = time.ParseInLocation("2006-01-02", startRaw, location)
		if err != nil {
			return time.Time{}, time.Time{}, &core.SyncError{Code: core.ErrInternal, Message: "start_date must be YYYY-MM-DD"}
		}
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, &core.SyncError{Code: core.ErrInternal, Message: "end_date must not be before start_date"}
	}
	if end.After(today) {
		return time.Time{}, time.Time{}, &core.SyncError{Code: core.ErrInternal, Message: "end_date must not be in the future"}
	}
	maxDays := spec.MaxRangeDays
	if maxDays <= 0 {
		maxDays = 366
	}
	if int(end.Sub(start).Hours()/24)+1 > maxDays {
		return time.Time{}, time.Time{}, &core.SyncError{Code: core.ErrInternal, Message: fmt.Sprintf("sync range must not exceed %d days", maxDays)}
	}
	return start, end, nil
}

func (e *Engine) execute(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.Timeout)
	defer cancel()

	run, err := e.store.GetSyncRun(ctx, e.site.ID, runID)
	if err != nil {
		e.logger.Error("read sync run", "run", runID, "error", err)
		return
	}
	if err := e.store.MarkRunRunning(ctx, runID); err != nil {
		e.logger.Error("start sync run", "run", runID, "error", err)
		return
	}

	provider, ok := e.registry.Get(run.Provider)
	if !ok {
		e.finish(run, core.SyncResult{}, &core.SyncError{Code: core.ErrNotConfigured, Message: "provider adapter is not available"})
		return
	}
	connection, err := e.store.GetConnection(ctx, e.site.ID, run.Provider)
	if err != nil || connection.CredentialRef.ID == "" {
		e.finish(run, core.SyncResult{}, &core.SyncError{Code: core.ErrNotConfigured, Message: "provider is not configured"})
		return
	}
	scope := core.Scope{SiteID: e.site.ID, Provider: run.Provider}
	credential, err := e.secrets.Open(ctx, scope, connection.CredentialRef, core.PurposeSync)
	if err != nil {
		e.finish(run, core.SyncResult{}, &core.SyncError{Code: core.ErrUnauthorized, Message: "credential is not available"})
		return
	}
	defer credential.Close()

	sink := &storeSink{store: e.store, siteID: e.site.ID, runID: runID}
	result, syncErr := provider.Sync(ctx, core.SyncRequest{
		Site:       e.site,
		Property:   core.Property{ConnectionID: connection.ID, Reference: connection.PropertyReference},
		Capability: run.Capability,
		StartDate:  run.StartDate,
		EndDate:    run.EndDate,
		Cursor:     run.Cursor,
	}, credential, sink)
	if syncErr == nil && ctx.Err() != nil {
		syncErr = &core.SyncError{Code: core.ErrTransient, Message: "sync run timed out"}
	}
	e.finish(run, result, syncErr)
}

func (e *Engine) finish(run core.SyncRun, result core.SyncResult, syncErr error) {
	// The run may have outlived its context; finishing state must still land.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status := core.SyncSucceeded
	code := core.ErrNone
	message := ""
	if syncErr != nil {
		code, message = classify(syncErr)
		status = core.SyncFailed
		if code == core.ErrPartial {
			status = core.SyncPartial
		}
	}
	if err := e.store.FinishRun(ctx, run.ID, status, result.Rows, result.Cursor, code, message); err != nil {
		e.logger.Error("finish sync run", "run", run.ID, "error", err)
	}

	if syncErr == nil || code == core.ErrPartial {
		now := e.now().UTC()
		var through *time.Time
		if !result.DataThroughDate.IsZero() {
			t := result.DataThroughDate
			through = &t
		}
		if err := e.store.UpdateConnectionOutcome(ctx, e.site.ID, run.Provider, &now, through, core.ErrNone, ""); err != nil {
			e.logger.Error("update connection outcome", "provider", run.Provider, "error", err)
		}
		return
	}
	if err := e.store.UpdateConnectionOutcome(ctx, e.site.ID, run.Provider, nil, nil, code, message); err != nil {
		e.logger.Error("update connection outcome", "provider", run.Provider, "error", err)
	}
}

// classify maps any error to a public (code, safe message) pair. Raw provider
// bodies never pass through: unclassified errors collapse to INTERNAL with a
// generic message.
func classify(err error) (core.ErrorCode, string) {
	var classified *core.SyncError
	if errors.As(err, &classified) {
		return classified.Code, truncate(classified.Message)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return core.ErrTransient, "sync run timed out"
	}
	return core.ErrInternal, "sync failed"
}

func truncate(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// Run is the scheduler loop: recover crashed runs, then schedule configured
// providers once per interval. Database unique indexes make this safe with
// manual triggers and multiple replicas.
func (e *Engine) Run(ctx context.Context) {
	e.tick(ctx)
	ticker := time.NewTicker(e.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.tick(ctx)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	if n, err := e.store.RequeueStaleRuns(ctx, e.cfg.Lease); err != nil {
		e.logger.Error("requeue stale sync runs", "error", err)
	} else if n > 0 {
		e.logger.Warn("requeued stale sync runs", "count", n)
	}
	for _, descriptor := range e.registry.Descriptors() {
		for _, spec := range descriptor.Capabilities {
			_, err := e.Create(ctx, CreateRequest{Provider: descriptor.Name, Capability: spec.Capability})
			var syncErr *core.SyncError
			if err != nil && !(errors.As(err, &syncErr) && syncErr.Code == core.ErrNotConfigured) {
				e.logger.Warn("schedule sync", "provider", descriptor.Name, "capability", spec.Capability, "error", err)
			}
		}
	}
}

// storeSink adapts the store to core.SnapshotSink for one run.
type storeSink struct {
	store  *store.Store
	siteID string
	runID  string
}

func (s *storeSink) Write(ctx context.Context, dataset string, rows []map[string]any) error {
	return s.store.UpsertReportRows(ctx, s.siteID, dataset, rows)
}

func (s *storeSink) Checkpoint(ctx context.Context, cursor string) error {
	return s.store.SetRunCursor(ctx, s.runID, cursor)
}
