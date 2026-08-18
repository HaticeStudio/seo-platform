// Package auth implements the generic auth boundary. The core recognizes no
// specific IAM product: anything that produces a core.Subject can guard the
// API. This package ships the API-key verifier and the explicit development
// mode; OIDC/JWT adapters plug in alongside it.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/HaticeStudio/seo-platform/core"
)

// Authenticator turns an HTTP request into a verified Subject or an error.
type Authenticator interface {
	Authenticate(r *http.Request) (core.Subject, error)
}

// APIKey verifies bearer keys against SHA-256 hashes from configuration.
// Plaintext keys are shown once at creation and only hashes are stored.
type APIKey struct {
	// hashes maps hex(sha256(key)) to the scopes that key grants.
	hashes map[string][]string
}

// NewAPIKey builds a verifier from "hexhash=scope1,scope2;hexhash=..." spec.
func NewAPIKey(spec string) (*APIKey, error) {
	a := &APIKey{hashes: make(map[string][]string)}
	for _, entry := range strings.Split(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		hash, scopes, found := strings.Cut(entry, "=")
		if !found || len(hash) != 64 {
			return nil, fmt.Errorf("api key entry must be <sha256-hex>=<scopes>")
		}
		a.hashes[strings.ToLower(hash)] = strings.Split(scopes, ",")
	}
	return a, nil
}

func (a *APIKey) Authenticate(r *http.Request) (core.Subject, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || token == r.Header.Get("Authorization") {
		return core.Subject{}, fmt.Errorf("missing bearer token")
	}
	sum := sha256.Sum256([]byte(token))
	got := hex.EncodeToString(sum[:])
	for hash, scopes := range a.hashes {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(got)) == 1 {
			return core.Subject{ID: "api-key:" + hash[:8], Issuer: "api-key", Scopes: scopes}, nil
		}
	}
	return core.Subject{}, fmt.Errorf("unknown api key")
}

// DevLoopback grants full scopes without credentials, but only to requests
// arriving from loopback, and only when explicitly enabled. The server must
// refuse to bind non-loopback addresses while this mode is on (ADR 0005).
type DevLoopback struct{}

func (DevLoopback) Authenticate(r *http.Request) (core.Subject, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return core.Subject{}, fmt.Errorf("development auth only accepts loopback requests")
	}
	return core.Subject{
		ID:     "dev-local",
		Issuer: "dev",
		Scopes: []string{core.ScopeRead, core.ScopeSync, core.ScopeConnectionsManage, core.ScopeSitesManage, core.ScopeMembersManage, core.ScopeAuditRead},
	}, nil
}
