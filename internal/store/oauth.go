package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// OAuthState is one pending authorization round-trip.
type OAuthState struct {
	State        string
	Provider     string
	SiteID       string
	PKCEVerifier string
	RedirectURI  string
	ExpiresAt    time.Time
}

// CreateOAuthState stores a pending state with the given TTL.
func (s *Store) CreateOAuthState(ctx context.Context, state OAuthState, ttl time.Duration) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO oauth_states (state, provider, site_id, pkce_verifier, redirect_uri, created_at, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		state.State, state.Provider, state.SiteID, state.PKCEVerifier, state.RedirectURI,
		now.Format(timeLayout), now.Add(ttl).Format(timeLayout))
	return err
}

var ErrOAuthStateInvalid = errors.New("oauth state is unknown, expired, or already used")

// ConsumeOAuthState atomically deletes and returns the state: single use,
// and expired rows are treated as absent.
func (s *Store) ConsumeOAuthState(ctx context.Context, state string) (OAuthState, error) {
	row := s.db.QueryRowContext(ctx, `
        DELETE FROM oauth_states
        WHERE state = ? AND expires_at > ?
        RETURNING state, provider, site_id, pkce_verifier, redirect_uri, expires_at`,
		state, time.Now().UTC().Format(timeLayout))
	var out OAuthState
	var expires string
	err := row.Scan(&out.State, &out.Provider, &out.SiteID, &out.PKCEVerifier, &out.RedirectURI, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthState{}, ErrOAuthStateInvalid
	}
	if err != nil {
		return OAuthState{}, err
	}
	out.ExpiresAt, _ = time.Parse(timeLayout, expires)
	// Opportunistically clear other expired rows.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM oauth_states WHERE expires_at <= ?`, time.Now().UTC().Format(timeLayout))
	return out, nil
}
