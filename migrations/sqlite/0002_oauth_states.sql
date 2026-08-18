-- 0002_oauth_states: short-lived OAuth authorization state. Rows are single
-- use and expire after minutes; the PKCE verifier lives only here and is
-- deleted on consumption (ADR 0005 retention rules).

CREATE TABLE IF NOT EXISTS oauth_states (
    state         TEXT PRIMARY KEY,
    provider      TEXT NOT NULL,
    site_id       TEXT NOT NULL,
    pkce_verifier TEXT NOT NULL,
    redirect_uri  TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    expires_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_oauth_states_expiry ON oauth_states (expires_at);
