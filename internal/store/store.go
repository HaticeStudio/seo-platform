// Package store is the SQLite persistence layer for the standalone server.
// PostgreSQL support arrives with its own migrations directory; the Go layer
// sticks to portable SQL so the two share code.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/migrations"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const timeLayout = time.RFC3339
const dateLayout = "2006-01-02"

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// pending migrations. Use ":memory:" for tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; serializing in the pool avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations.SQLite, "sqlite")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		raw, err := fs.ReadFile(migrations.SQLite, "sqlite/"+name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(raw)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().UTC().Format(timeLayout)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Ping reports readiness.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// EnsureSite upserts the configured site. Single-site deployments call this
// at startup from configuration.
func (s *Store) EnsureSite(ctx context.Context, site core.Site) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO sites (id, public_url, sitemap_url, timezone) VALUES (?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET public_url = excluded.public_url,
            sitemap_url = excluded.sitemap_url, timezone = excluded.timezone`,
		site.ID, site.PublicURL, site.SitemapURL, site.Timezone)
	return err
}

// EnsureConnection creates the connection row for a provider on a site if it
// does not exist yet, so the Console always has a row to show state on.
func (s *Store) EnsureConnection(ctx context.Context, siteID, provider string) error {
	now := time.Now().UTC().Format(timeLayout)
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO provider_connections (id, site_id, provider, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(site_id, provider) DO NOTHING`,
		uuid.NewString(), siteID, provider, now, now)
	return err
}

func (s *Store) GetConnection(ctx context.Context, siteID, provider string) (core.ProviderConnection, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT id, site_id, provider, credential_ref, credential_type, enabled,
               property_reference, last_success_at, data_through_date,
               last_error_code, last_error_message, created_at, updated_at
        FROM provider_connections WHERE site_id = ? AND provider = ?`, siteID, provider)
	return scanConnection(row)
}

func (s *Store) ListConnections(ctx context.Context, siteID string) ([]core.ProviderConnection, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, site_id, provider, credential_ref, credential_type, enabled,
               property_reference, last_success_at, data_through_date,
               last_error_code, last_error_message, created_at, updated_at
        FROM provider_connections WHERE site_id = ? ORDER BY provider`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ProviderConnection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConfigureConnection sets credential ref, property, and enablement in one
// step (compare-and-swap semantics for rotation live in the sync engine).
func (s *Store) ConfigureConnection(ctx context.Context, siteID, provider string, ref core.CredentialRef, property string, enabled bool) error {
	now := time.Now().UTC().Format(timeLayout)
	res, err := s.db.ExecContext(ctx, `
        UPDATE provider_connections
        SET credential_ref = ?, credential_type = ?, property_reference = ?, enabled = ?, updated_at = ?
        WHERE site_id = ? AND provider = ?`,
		ref.ID, ref.Type, property, boolInt(enabled), now, siteID, provider)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("connection %s/%s does not exist", siteID, provider)
	}
	return nil
}

// ConfigureConnectionCAS swaps a credential only when the connection still
// references expected. It prevents concurrent reconfiguration from revoking
// the credential another administrator just installed.
func (s *Store) ConfigureConnectionCAS(ctx context.Context, siteID, provider string, expected, replacement core.CredentialRef, property string, enabled bool) (bool, error) {
	now := time.Now().UTC().Format(timeLayout)
	res, err := s.db.ExecContext(ctx, `
        UPDATE provider_connections
        SET credential_ref = ?, credential_type = ?, property_reference = ?, enabled = ?, updated_at = ?
        WHERE site_id = ? AND provider = ? AND credential_ref = ?`,
		replacement.ID, replacement.Type, property, boolInt(enabled), now,
		siteID, provider, expected.ID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) UpdateConnectionOutcome(ctx context.Context, siteID, provider string, successAt *time.Time, dataThrough *time.Time, code core.ErrorCode, message string) error {
	now := time.Now().UTC().Format(timeLayout)
	if successAt != nil {
		var through any
		if dataThrough != nil {
			through = dataThrough.Format(dateLayout)
		}
		_, err := s.db.ExecContext(ctx, `
            UPDATE provider_connections
            SET last_success_at = ?, data_through_date = COALESCE(?, data_through_date),
                last_error_code = '', last_error_message = '', updated_at = ?
            WHERE site_id = ? AND provider = ?`,
			successAt.UTC().Format(timeLayout), through, now, siteID, provider)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
        UPDATE provider_connections SET last_error_code = ?, last_error_message = ?, updated_at = ?
        WHERE site_id = ? AND provider = ?`,
		string(code), message, now, siteID, provider)
	return err
}

type scannable interface{ Scan(dest ...any) error }

func scanConnection(row scannable) (core.ProviderConnection, error) {
	var c core.ProviderConnection
	var enabled int
	var lastSuccess, dataThrough sql.NullString
	var refID, refType string
	var createdAt, updatedAt string
	var code string
	err := row.Scan(&c.ID, &c.SiteID, &c.Provider, &refID, &refType, &enabled,
		&c.PropertyReference, &lastSuccess, &dataThrough, &code, &c.LastErrorMessage,
		&createdAt, &updatedAt)
	if err != nil {
		return c, err
	}
	c.CredentialRef = core.CredentialRef{ID: refID, Type: refType}
	c.Enabled = enabled != 0
	c.LastErrorCode = core.ErrorCode(code)
	c.CreatedAt, _ = time.Parse(timeLayout, createdAt)
	c.UpdatedAt, _ = time.Parse(timeLayout, updatedAt)
	if lastSuccess.Valid {
		if t, err := time.Parse(timeLayout, lastSuccess.String); err == nil {
			c.LastSuccessAt = &t
		}
	}
	if dataThrough.Valid {
		if t, err := time.Parse(dateLayout, dataThrough.String); err == nil {
			c.DataThroughDate = &t
		}
	}
	return c, nil
}

// CreateSyncRun inserts a QUEUED run. It returns (existing, false, nil) when
// the idempotency key or the single-flight index already holds a run.
func (s *Store) CreateSyncRun(ctx context.Context, run core.SyncRun, siteID string) (core.SyncRun, bool, error) {
	now := time.Now().UTC()
	run.ID = uuid.NewString()
	run.Status = core.SyncQueued
	run.StartedAt = now
	run.CreatedAt = now
	run.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO sync_runs (id, provider, site_id, capability, start_date, end_date, status,
            triggered_by, idempotency_key, started_at, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.Provider, siteID, string(run.Capability),
		run.StartDate.Format(dateLayout), run.EndDate.Format(dateLayout), string(run.Status),
		run.TriggeredBy, run.IdempotencyKey,
		now.Format(timeLayout), now.Format(timeLayout), now.Format(timeLayout))
	if err != nil {
		if isUniqueViolation(err) {
			existing, lookupErr := s.findConflictingRun(ctx, run, siteID)
			if lookupErr == nil {
				return existing, false, nil
			}
			return core.SyncRun{}, false, lookupErr
		}
		return core.SyncRun{}, false, err
	}
	return run, true, nil
}

func (s *Store) findConflictingRun(ctx context.Context, run core.SyncRun, siteID string) (core.SyncRun, error) {
	existing, err := s.getRunWhere(ctx, `site_id = ? AND idempotency_key = ?`, siteID, run.IdempotencyKey)
	if err == nil {
		return existing, nil
	}
	return s.getRunWhere(ctx,
		`site_id = ? AND provider = ? AND capability = ? AND status IN ('QUEUED','RUNNING') ORDER BY started_at DESC`,
		siteID, run.Provider, string(run.Capability))
}

func (s *Store) GetSyncRun(ctx context.Context, siteID, id string) (core.SyncRun, error) {
	return s.getRunWhere(ctx, `site_id = ? AND id = ?`, siteID, id)
}

func (s *Store) getRunWhere(ctx context.Context, where string, args ...any) (core.SyncRun, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT id, provider, capability, start_date, end_date, status, rows_synced, cursor,
               triggered_by, idempotency_key, error_code, error_message,
               started_at, finished_at, created_at, updated_at
        FROM sync_runs WHERE `+where+` LIMIT 1`, args...)
	return scanRun(row)
}

func (s *Store) ListSyncRuns(ctx context.Context, siteID, provider string, limit int) ([]core.SyncRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, provider, capability, start_date, end_date, status, rows_synced, cursor,
               triggered_by, idempotency_key, error_code, error_message,
	               started_at, finished_at, created_at, updated_at FROM sync_runs WHERE site_id = ?`
	args := []any{siteID}
	if provider != "" {
		query += ` AND provider = ?`
		args = append(args, provider)
	}
	query += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.SyncRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRun(row scannable) (core.SyncRun, error) {
	var r core.SyncRun
	var capability, status, code string
	var start, end, startedAt, createdAt, updatedAt string
	var finishedAt sql.NullString
	err := row.Scan(&r.ID, &r.Provider, &capability, &start, &end, &status, &r.RowsSynced,
		&r.Cursor, &r.TriggeredBy, &r.IdempotencyKey, &code, &r.ErrorMessage,
		&startedAt, &finishedAt, &createdAt, &updatedAt)
	if err != nil {
		return r, err
	}
	r.Capability = core.Capability(capability)
	r.Status = core.SyncRunStatus(status)
	r.ErrorCode = core.ErrorCode(code)
	r.StartDate, _ = time.Parse(dateLayout, start)
	r.EndDate, _ = time.Parse(dateLayout, end)
	r.StartedAt, _ = time.Parse(timeLayout, startedAt)
	r.CreatedAt, _ = time.Parse(timeLayout, createdAt)
	r.UpdatedAt, _ = time.Parse(timeLayout, updatedAt)
	if finishedAt.Valid {
		if t, err := time.Parse(timeLayout, finishedAt.String); err == nil {
			r.FinishedAt = &t
		}
	}
	return r, nil
}

func (s *Store) MarkRunRunning(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(timeLayout)
	_, err := s.db.ExecContext(ctx, `
        UPDATE sync_runs SET status = 'RUNNING', started_at = ?, updated_at = ? WHERE id = ?`,
		now, now, id)
	return err
}

// FinishRun records the terminal state of a run.
func (s *Store) FinishRun(ctx context.Context, id string, status core.SyncRunStatus, rows int64, cursor string, code core.ErrorCode, message string) error {
	now := time.Now().UTC().Format(timeLayout)
	_, err := s.db.ExecContext(ctx, `
        UPDATE sync_runs SET status = ?, rows_synced = ?, cursor = ?, error_code = ?,
            error_message = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
		string(status), rows, cursor, string(code), message, now, now, id)
	return err
}

// RequeueStaleRuns flips runs stuck in RUNNING longer than lease back to
// FAILED/stale so the scheduler can retry them. It is the worker-crash
// recovery path.
func (s *Store) RequeueStaleRuns(ctx context.Context, lease time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-lease).Format(timeLayout)
	res, err := s.db.ExecContext(ctx, `
        UPDATE sync_runs SET status = 'FAILED', error_code = 'TRANSIENT',
            error_message = 'worker lease expired', updated_at = ?
        WHERE status = 'RUNNING' AND started_at < ?`,
		time.Now().UTC().Format(timeLayout), cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpsertReportRows writes one batch into the generic normalized store. Every
// row must carry a "_key" string uniquely identifying it within the dataset.
func (s *Store) UpsertReportRows(ctx context.Context, siteID, dataset string, rows []map[string]any) error {
	if siteID == "" || dataset == "" {
		return fmt.Errorf("site and dataset are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(timeLayout)
	for _, row := range rows {
		key, _ := row["_key"].(string)
		if key == "" {
			return fmt.Errorf("report row in dataset %s is missing _key", dataset)
		}
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO report_rows (site_id, dataset, row_key, data, updated_at) VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(site_id, dataset, row_key) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
			siteID, dataset, key, string(data), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type ReportRow struct {
	Dataset   string
	Key       string
	Data      map[string]any
	UpdatedAt time.Time
}

func (s *Store) ListReportDatasets(ctx context.Context, siteID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT DISTINCT dataset FROM report_rows WHERE site_id = ? ORDER BY dataset`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var dataset string
		if err := rows.Scan(&dataset); err != nil {
			return nil, err
		}
		out = append(out, dataset)
	}
	return out, rows.Err()
}

func (s *Store) ListReportRows(ctx context.Context, siteID, dataset string, limit int) ([]ReportRow, error) {
	if strings.TrimSpace(dataset) == "" {
		return nil, fmt.Errorf("dataset is required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT dataset, row_key, data, updated_at
        FROM report_rows WHERE site_id = ? AND dataset = ?
        ORDER BY row_key DESC LIMIT ?`, siteID, dataset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportRow
	for rows.Next() {
		var row ReportRow
		var raw, updated string
		if err := rows.Scan(&row.Dataset, &row.Key, &raw, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &row.Data); err != nil {
			return nil, fmt.Errorf("decode report row: %w", err)
		}
		row.UpdatedAt, _ = time.Parse(timeLayout, updated)
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetRunCursor persists a resume checkpoint mid-run.
func (s *Store) SetRunCursor(ctx context.Context, id, cursor string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sync_runs SET cursor = ?, updated_at = ? WHERE id = ?`,
		cursor, time.Now().UTC().Format(timeLayout), id)
	return err
}

func (s *Store) AppendAudit(ctx context.Context, event core.AuditEvent) error {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO audit_events (id, actor, action, target, result, request_id, at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.Actor, event.Action, event.Target, event.Result, event.RequestID,
		event.At.UTC().Format(timeLayout))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
