package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/store"
	"golang.org/x/oauth2"
)

// OAuthApp is the deployment's OAuth client for one provider: which
// authorization server to talk to and which scopes to request. Client
// secrets arrive from deployment configuration and are used server-side
// only — the browser sees the authorize URL and nothing else.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
}

const oauthStateTTL = 10 * time.Minute

// WithOAuthApps registers per-provider OAuth clients. Providers without an
// entry simply have no OAuth endpoints.
func (s *Server) WithOAuthApps(apps map[string]OAuthApp) *Server {
	s.oauthApps = apps
	return s
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request, subject core.Subject) {
	provider := r.PathValue("provider")
	registered, exists := s.registry.Get(provider)
	if !exists || !containsCredentialType(registered.Descriptor().CredentialTypes, "oauth2") {
		writeError(w, http.StatusNotFound, "provider does not support OAuth")
		return
	}
	app, ok := s.oauthApps[provider]
	if !ok {
		writeError(w, http.StatusNotFound, "provider has no OAuth client configured")
		return
	}
	var req struct {
		RedirectURI string `json:"redirect_uri"`
		ReturnTo    string `json:"return_to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	redirect, err := url.Parse(strings.TrimSpace(req.RedirectURI))
	if err != nil || !redirect.IsAbs() || redirect.User != nil || redirect.Fragment != "" || (redirect.Scheme != "https" && !isLoopbackHost(redirect.Hostname())) {
		writeError(w, http.StatusBadRequest, "redirect_uri must be absolute and https (or loopback)")
		return
	}
	expectedCallback := strings.TrimRight(s.platformURL, "/") + "/oauth/callback"
	if redirect.String() != expectedCallback {
		writeError(w, http.StatusBadRequest, "redirect_uri must match the configured OAuth callback")
		return
	}
	returnTo, err := safeReturnTo(req.ReturnTo)
	if err != nil {
		writeError(w, http.StatusBadRequest, "return_to must be a local absolute path")
		return
	}

	state, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create authorization state")
		return
	}
	verifier, err := randomToken(48)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create authorization state")
		return
	}
	if err := s.store.CreateOAuthState(r.Context(), store.OAuthState{
		State:        state,
		Provider:     provider,
		SiteID:       s.site.ID,
		SubjectID:    subject.ID,
		PKCEVerifier: verifier,
		RedirectURI:  redirect.String(),
		ReturnTo:     returnTo,
	}, oauthStateTTL); err != nil {
		s.logger.Error("create oauth state", "provider", provider, "error", err)
		writeError(w, http.StatusInternalServerError, "create oauth state")
		return
	}

	challenge := base64.RawURLEncoding.EncodeToString(func() []byte {
		sum := sha256.Sum256([]byte(verifier))
		return sum[:]
	}())
	config := oauth2.Config{
		ClientID: app.ClientID,
		Endpoint: oauth2.Endpoint{AuthURL: app.AuthURL},
		Scopes:   app.Scopes,
	}
	authorizeURL := config.AuthCodeURL(state,
		oauth2.SetAuthURLParam("redirect_uri", redirect.String()),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("access_type", "offline"),
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	s.audit(r, subject, "connection.oauth.start", provider, "ok")
	writeJSON(w, http.StatusOK, map[string]string{"authorize_url": authorizeURL, "state": state})
}

func containsCredentialType(types []string, want string) bool {
	for _, credentialType := range types {
		if credentialType == want {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}

func (s *Server) oauthComplete(w http.ResponseWriter, r *http.Request, subject core.Subject) {
	provider := r.PathValue("provider")
	app, ok := s.oauthApps[provider]
	if !ok {
		writeError(w, http.StatusNotFound, "provider has no OAuth client configured")
		return
	}
	var req struct {
		State string `json:"state"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	pending, err := s.store.ConsumeOAuthState(r.Context(), strings.TrimSpace(req.State), provider, s.site.ID, subject.ID)
	if err != nil {
		if errors.Is(err, store.ErrOAuthStateInvalid) {
			s.audit(r, subject, "connection.oauth.complete", provider, "invalid_state")
			writeError(w, http.StatusBadRequest, "authorization state is invalid or expired; start again")
			return
		}
		writeError(w, http.StatusInternalServerError, "read oauth state")
		return
	}
	if pending.Provider != provider || pending.SiteID != s.site.ID || pending.SubjectID != subject.ID {
		s.audit(r, subject, "connection.oauth.complete", provider, "state_mismatch")
		writeError(w, http.StatusBadRequest, "authorization state does not match this provider")
		return
	}

	config := oauth2.Config{
		ClientID:     app.ClientID,
		ClientSecret: app.ClientSecret,
		Endpoint:     oauth2.Endpoint{TokenURL: app.TokenURL},
		RedirectURL:  pending.RedirectURI,
	}
	token, err := config.Exchange(r.Context(), strings.TrimSpace(req.Code),
		oauth2.SetAuthURLParam("code_verifier", pending.PKCEVerifier))
	if err != nil {
		s.audit(r, subject, "connection.oauth.complete", provider, "exchange_failed")
		writeError(w, http.StatusBadGateway, "authorization code exchange failed")
		return
	}
	if token.RefreshToken == "" {
		writeError(w, http.StatusBadGateway, "authorization server returned no refresh token; re-consent is required")
		return
	}

	material, err := core.OAuthMaterial{
		ClientID:     app.ClientID,
		ClientSecret: app.ClientSecret,
		TokenURL:     app.TokenURL,
		RefreshToken: token.RefreshToken,
	}.Marshal()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode credential")
		return
	}
	connection, err := s.store.GetConnection(r.Context(), s.site.ID, provider)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection does not exist")
		return
	}
	scope := core.Scope{SiteID: s.site.ID, Provider: provider}
	ref, err := s.secrets.Put(r.Context(), scope, core.SecretMaterial{Type: "oauth2", Bytes: material})
	if err != nil {
		s.logger.Error("store oauth credential", "provider", provider, "error", err)
		writeError(w, http.StatusInternalServerError, "store credential")
		return
	}
	s.audit(r, subject, "connection.oauth.complete", provider, "ok")
	s.finishCredentialUpdate(w, r, subject, provider, connection, ref, map[string]any{"return_to": pending.ReturnTo})
}

// safeReturnTo accepts a path owned by this Console only. Storing it inside
// the single-use OAuth row binds navigation to the authorization request and
// prevents an attacker from turning the callback into an open redirect.
func safeReturnTo(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "/", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return "", errors.New("unsafe return path")
	}
	return parsed.RequestURI(), nil
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
