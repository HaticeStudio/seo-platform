-- 0001_init: single-site core schema.
-- Business tables hold only opaque credential refs — never credential material.
-- Migrations are forward-only and idempotent (VERSIONING.md).

CREATE TABLE IF NOT EXISTS sites (
    id          TEXT PRIMARY KEY,
    public_url  TEXT NOT NULL,
    sitemap_url TEXT NOT NULL,
    timezone    TEXT NOT NULL DEFAULT 'UTC'
);

CREATE TABLE IF NOT EXISTS provider_connections (
    id                 TEXT PRIMARY KEY,
    site_id            TEXT NOT NULL REFERENCES sites(id),
    provider           TEXT NOT NULL,
    credential_ref     TEXT NOT NULL DEFAULT '',
    credential_type    TEXT NOT NULL DEFAULT '',
    enabled            INTEGER NOT NULL DEFAULT 0,
    property_reference TEXT NOT NULL DEFAULT '',
    last_success_at    TEXT,
    data_through_date  TEXT,
    last_error_code    TEXT NOT NULL DEFAULT '',
    last_error_message TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE (site_id, provider)
);

CREATE TABLE IF NOT EXISTS sync_runs (
    id              TEXT PRIMARY KEY,
    provider        TEXT NOT NULL,
    site_id         TEXT NOT NULL,
    capability      TEXT NOT NULL,
    start_date      TEXT NOT NULL,
    end_date        TEXT NOT NULL,
    status          TEXT NOT NULL,
    rows_synced     INTEGER NOT NULL DEFAULT 0,
    cursor          TEXT NOT NULL DEFAULT '',
    triggered_by    TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL,
    error_code      TEXT NOT NULL DEFAULT '',
    error_message   TEXT NOT NULL DEFAULT '',
    started_at      TEXT NOT NULL,
    finished_at     TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

-- Idempotent client retries: one run per key.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sync_runs_idempotency
    ON sync_runs (idempotency_key);

-- Single-flight: at most one queued/running run per provider+capability+site.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sync_runs_single_flight
    ON sync_runs (site_id, provider, capability)
    WHERE status IN ('QUEUED', 'RUNNING');

CREATE INDEX IF NOT EXISTS idx_sync_runs_started
    ON sync_runs (started_at DESC);

-- Normalized snapshot staging. Provider packages may ship their own richer
-- tables; the runtime guarantees this generic upsert store exists.
CREATE TABLE IF NOT EXISTS report_rows (
    dataset    TEXT NOT NULL,
    row_key    TEXT NOT NULL,
    data       TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (dataset, row_key)
);

CREATE TABLE IF NOT EXISTS audit_events (
    id         TEXT PRIMARY KEY,
    actor      TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    result     TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    at         TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_events_at ON audit_events (at DESC);
