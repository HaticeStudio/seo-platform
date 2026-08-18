-- Bind OAuth state to the authenticated administrator and make report rows
-- site-aware before the first public release.

ALTER TABLE oauth_states ADD COLUMN subject_id TEXT NOT NULL DEFAULT '';

ALTER TABLE report_rows RENAME TO report_rows_unscoped;

CREATE TABLE report_rows (
    site_id    TEXT NOT NULL REFERENCES sites(id),
    dataset    TEXT NOT NULL,
    row_key    TEXT NOT NULL,
    data       TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (site_id, dataset, row_key)
);

INSERT INTO report_rows (site_id, dataset, row_key, data, updated_at)
SELECT 'default', dataset, row_key, data, updated_at
FROM report_rows_unscoped
WHERE EXISTS (SELECT 1 FROM sites WHERE id = 'default');

DROP TABLE report_rows_unscoped;

CREATE INDEX idx_report_rows_dataset
    ON report_rows (site_id, dataset, updated_at DESC);

DROP INDEX idx_sync_runs_idempotency;
CREATE UNIQUE INDEX idx_sync_runs_idempotency
    ON sync_runs (site_id, idempotency_key);

DROP INDEX idx_sync_runs_started;
CREATE INDEX idx_sync_runs_started
    ON sync_runs (site_id, started_at DESC);
