package core

import "context"

// CredentialRef is the only credential-shaped value that may appear in
// business tables, API responses, logs, or audit events. It is opaque: it
// carries no secret material and no way to derive it.
type CredentialRef struct {
	ID   string
	Type string // e.g. "oauth2", "service_account_json", "api_key"
}

// SecretMaterial is credential content in transit to a SecretStore. It exists
// only long enough to be stored and must never be persisted elsewhere.
type SecretMaterial struct {
	Type  string
	Bytes []byte
}

// AccessPurpose says why a handle is being opened; stores may audit or
// restrict by it.
type AccessPurpose string

const (
	PurposeSync   AccessPurpose = "sync"
	PurposeTest   AccessPurpose = "test"
	PurposeRevoke AccessPurpose = "revoke"
)

// Scope binds a secret to its owner. Workspace is empty in single-site mode;
// when multi-site is enabled the store must include it in its storage path or
// encryption context so cross-scope opens fail.
type Scope struct {
	Workspace string
	SiteID    string
	Provider  string
}

// CredentialHandle is a short-lived, in-process view of a secret. It is
// deliberately an interface without String or marshal methods, so handles
// cannot be logged or serialized by accident.
type CredentialHandle interface {
	// Material exposes the secret bytes for immediate use. Callers must not
	// retain the slice beyond the current operation.
	Material() SecretMaterial
	// Close releases the handle. Using a handle after Close is a bug.
	Close()
}

// SecretStore is the only component that touches credential material at rest.
// Production deployments must plug in a real store (cloud secret manager,
// Vault, or envelope-encrypted DB); the in-memory and file stores shipped in
// this repo are for development and single-machine use.
type SecretStore interface {
	Put(ctx context.Context, scope Scope, material SecretMaterial) (CredentialRef, error)
	Open(ctx context.Context, ref CredentialRef, purpose AccessPurpose) (CredentialHandle, error)
	// Rotate atomically replaces the material behind ref. The ref stays valid
	// so business rows holding it need no update.
	Rotate(ctx context.Context, ref CredentialRef, replacement SecretMaterial) error
	Revoke(ctx context.Context, ref CredentialRef) error
}
