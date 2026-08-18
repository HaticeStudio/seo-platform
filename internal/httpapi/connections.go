package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/HaticeStudio/seo-platform/core"
)

// Connection management: credentials in, opaque state out. Material enters
// through this API exactly once, goes straight into the SecretStore, and is
// never echoed back.

type setCredentialJSON struct {
	CredentialType string `json:"credential_type"`
	// Material is the raw credential value: the API key text or the
	// service-account JSON document. OAuth connections use the oauth
	// endpoints instead of this field.
	Material string `json:"material"`
}

func (s *Server) setCredential(w http.ResponseWriter, r *http.Request, subject core.Subject) {
	provider := r.PathValue("provider")
	registered, ok := s.registry.Get(provider)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown provider")
		return
	}
	var req setCredentialJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	credentialType := strings.TrimSpace(req.CredentialType)
	if !supportsCredentialType(registered.Descriptor(), credentialType) {
		writeError(w, http.StatusBadRequest, "provider does not accept this credential type")
		return
	}
	if strings.TrimSpace(req.Material) == "" {
		writeError(w, http.StatusBadRequest, "credential material is required")
		return
	}

	connection, err := s.store.GetConnection(r.Context(), s.site.ID, provider)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection does not exist")
		return
	}
	scope := core.Scope{SiteID: s.site.ID, Provider: provider}
	ref, err := s.secrets.Put(r.Context(), scope, core.SecretMaterial{Type: credentialType, Bytes: []byte(req.Material)})
	if err != nil {
		s.logger.Error("store credential", "provider", provider, "error", err)
		writeError(w, http.StatusInternalServerError, "store credential")
		return
	}
	s.finishCredentialUpdate(w, r, subject, provider, connection, ref, nil)
}

// finishCredentialUpdate swaps the connection to ref, revokes the previous
// secret, and returns discovered properties best-effort. Shared by manual
// credential entry and the OAuth completion path.
func (s *Server) finishCredentialUpdate(w http.ResponseWriter, r *http.Request, subject core.Subject, provider string, previous core.ProviderConnection, ref core.CredentialRef, extra map[string]any) {
	scope := core.Scope{SiteID: s.site.ID, Provider: provider}
	properties, discoverErr := s.discoverProperties(r.Context(), provider, ref)
	discoveryMessage := ""
	var classified *core.SyncError
	discoveryUnsupported := errors.As(discoverErr, &classified) && classified.Code == core.ErrUnsupported
	if discoverErr != nil && !discoveryUnsupported {
		_ = s.secrets.Revoke(context.WithoutCancel(r.Context()), scope, ref)
		s.audit(r, subject, "connection.credential.set", provider, "validation_failed")
		writeError(w, http.StatusBadGateway, discoverErr.Error())
		return
	}
	if discoveryUnsupported {
		discoveryMessage = classified.Message
	}
	if previous.PropertyReference != "" {
		handle, openErr := s.secrets.Open(r.Context(), scope, ref, core.PurposeTest)
		if openErr != nil {
			_ = s.secrets.Revoke(context.WithoutCancel(r.Context()), scope, ref)
			writeError(w, http.StatusConflict, "credential is not available")
			return
		}
		testErr := registeredTest(s.registry, provider, r.Context(), s.site, previous.PropertyReference, handle)
		handle.Close()
		if testErr != nil {
			_ = s.secrets.Revoke(context.WithoutCancel(r.Context()), scope, ref)
			s.audit(r, subject, "connection.credential.set", provider, "validation_failed")
			writeError(w, http.StatusBadGateway, publicError(testErr).Error())
			return
		}
	}
	enabled := previous.PropertyReference != ""
	swapped, err := s.store.ConfigureConnectionCAS(r.Context(), s.site.ID, provider, previous.CredentialRef, ref, previous.PropertyReference, enabled)
	if err != nil {
		_ = s.secrets.Revoke(context.WithoutCancel(r.Context()), scope, ref)
		s.logger.Error("configure connection", "provider", provider, "error", err)
		writeError(w, http.StatusInternalServerError, "configure connection")
		return
	}
	if !swapped {
		_ = s.secrets.Revoke(context.WithoutCancel(r.Context()), scope, ref)
		writeError(w, http.StatusConflict, "connection changed while the credential was being validated; try again")
		return
	}
	if previous.CredentialRef.ID != "" {
		// Old material is dead the moment the swap lands.
		if err := s.secrets.Revoke(context.WithoutCancel(r.Context()), scope, previous.CredentialRef); err != nil {
			s.logger.Error("revoke replaced credential", "provider", provider, "error", err)
		}
	}
	s.audit(r, subject, "connection.credential.set", provider, "ok")

	response := map[string]any{"configured": true, "properties": properties}
	for key, value := range extra {
		response[key] = value
	}
	if discoveryMessage != "" {
		response["property_discovery_error"] = discoveryMessage
	}
	writeJSON(w, http.StatusOK, response)
}

func registeredTest(registry interface {
	Get(string) (core.Provider, bool)
}, provider string, ctx context.Context, site core.Site, reference string, handle core.CredentialHandle) error {
	registered, ok := registry.Get(provider)
	if !ok {
		return errors.New("unknown provider")
	}
	return registered.Test(ctx, site, core.Property{Reference: reference}, handle)
}

func (s *Server) discoverProperties(ctx context.Context, provider string, ref core.CredentialRef) ([]map[string]string, error) {
	registered, ok := s.registry.Get(provider)
	if !ok {
		return nil, errors.New("unknown provider")
	}
	scope := core.Scope{SiteID: s.site.ID, Provider: provider}
	handle, err := s.secrets.Open(ctx, scope, ref, core.PurposeTest)
	if err != nil {
		return nil, errors.New("credential is not available")
	}
	defer handle.Close()
	discovered, err := registered.DiscoverProperties(ctx, handle)
	if err != nil {
		return nil, publicError(err)
	}
	out := make([]map[string]string, 0, len(discovered))
	for _, property := range discovered {
		out = append(out, map[string]string{
			"reference":    property.Reference,
			"display_name": property.DisplayName,
		})
	}
	return out, nil
}

func (s *Server) listProperties(w http.ResponseWriter, r *http.Request, _ core.Subject) {
	provider := r.PathValue("provider")
	connection, err := s.store.GetConnection(r.Context(), s.site.ID, provider)
	if err != nil || connection.CredentialRef.ID == "" {
		writeError(w, http.StatusConflict, "provider is not configured")
		return
	}
	properties, err := s.discoverProperties(r.Context(), provider, connection.CredentialRef)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"properties": properties})
}

func (s *Server) setProperty(w http.ResponseWriter, r *http.Request, subject core.Subject) {
	provider := r.PathValue("provider")
	var req struct {
		PropertyReference string `json:"property_reference"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	reference := strings.TrimSpace(req.PropertyReference)
	if reference == "" {
		writeError(w, http.StatusBadRequest, "property_reference is required")
		return
	}
	connection, err := s.store.GetConnection(r.Context(), s.site.ID, provider)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection does not exist")
		return
	}
	if connection.CredentialRef.ID == "" {
		writeError(w, http.StatusConflict, "set a credential before choosing a property")
		return
	}
	registered, ok := s.registry.Get(provider)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown provider")
		return
	}
	scope := core.Scope{SiteID: s.site.ID, Provider: provider}
	handle, err := s.secrets.Open(r.Context(), scope, connection.CredentialRef, core.PurposeTest)
	if err != nil {
		writeError(w, http.StatusConflict, "credential is not available")
		return
	}
	testErr := registered.Test(r.Context(), s.site, core.Property{Reference: reference}, handle)
	handle.Close()
	if testErr != nil {
		s.audit(r, subject, "connection.property.set", provider+"/"+reference, "validation_failed")
		writeError(w, http.StatusBadGateway, publicError(testErr).Error())
		return
	}
	if err := s.store.ConfigureConnection(r.Context(), s.site.ID, provider, connection.CredentialRef, reference, true); err != nil {
		writeError(w, http.StatusInternalServerError, "configure connection")
		return
	}
	s.audit(r, subject, "connection.property.set", provider+"/"+reference, "ok")
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "property_reference": reference})
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request, subject core.Subject) {
	provider := r.PathValue("provider")
	registered, ok := s.registry.Get(provider)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown provider")
		return
	}
	connection, err := s.store.GetConnection(r.Context(), s.site.ID, provider)
	if err != nil || connection.CredentialRef.ID == "" {
		writeError(w, http.StatusConflict, "provider is not configured")
		return
	}
	scope := core.Scope{SiteID: s.site.ID, Provider: provider}
	handle, err := s.secrets.Open(r.Context(), scope, connection.CredentialRef, core.PurposeTest)
	if err != nil {
		writeError(w, http.StatusConflict, "credential is not available")
		return
	}
	defer handle.Close()
	testErr := registered.Test(r.Context(), s.site, core.Property{Reference: connection.PropertyReference}, handle)
	result := "ok"
	if testErr != nil {
		result = "failed"
	}
	s.audit(r, subject, "connection.test", provider, result)
	if testErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": publicError(testErr).Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) revokeConnection(w http.ResponseWriter, r *http.Request, subject core.Subject) {
	provider := r.PathValue("provider")
	registered, ok := s.registry.Get(provider)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown provider")
		return
	}
	connection, err := s.store.GetConnection(r.Context(), s.site.ID, provider)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection does not exist")
		return
	}
	if connection.CredentialRef.ID != "" {
		// Best-effort provider-side revocation first, then the secret dies.
		scope := core.Scope{SiteID: s.site.ID, Provider: provider}
		if handle, openErr := s.secrets.Open(r.Context(), scope, connection.CredentialRef, core.PurposeRevoke); openErr == nil {
			if revokeErr := registered.Revoke(r.Context(), handle); revokeErr != nil {
				s.logger.Warn("provider-side revoke failed", "provider", provider, "error", revokeErr)
			}
			handle.Close()
		}
		if err := s.secrets.Revoke(r.Context(), scope, connection.CredentialRef); err != nil {
			s.logger.Error("revoke credential", "provider", provider, "error", err)
			writeError(w, http.StatusInternalServerError, "revoke credential")
			return
		}
	}
	if err := s.store.ConfigureConnection(r.Context(), s.site.ID, provider, core.CredentialRef{}, "", false); err != nil {
		writeError(w, http.StatusInternalServerError, "configure connection")
		return
	}
	s.audit(r, subject, "connection.revoke", provider, "ok")
	writeJSON(w, http.StatusOK, map[string]any{"configured": false})
}

func (s *Server) audit(r *http.Request, subject core.Subject, action, target, result string) {
	event := core.AuditEvent{Actor: subject.ID, Action: action, Target: target, Result: result}
	if err := s.store.AppendAudit(context.WithoutCancel(r.Context()), event); err != nil {
		s.logger.Error("append audit event", "action", action, "error", err)
	}
}

func supportsCredentialType(descriptor core.Descriptor, credentialType string) bool {
	for _, supported := range descriptor.CredentialTypes {
		if supported == credentialType {
			return true
		}
	}
	return false
}

// publicError keeps classified messages and collapses everything else, so raw
// provider or store errors never reach a client.
func publicError(err error) error {
	var classified *core.SyncError
	if errors.As(err, &classified) {
		return classified
	}
	return errors.New("provider request failed")
}
