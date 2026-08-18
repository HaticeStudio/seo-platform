package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/google/uuid"
)

// File is the local Quick Start SecretStore: AES-256-GCM encrypted files in a
// 0700 directory, one file per credential, keyed by a 32-byte master key the
// operator supplies out of band (never stored next to the payloads).
//
// It is suitable for a single-machine deployment; anything larger should use
// a cloud secret manager or Vault behind core.SecretStore.
type File struct {
	mu   sync.Mutex
	dir  string
	aead cipher.AEAD
}

type filePayload struct {
	Scope    core.Scope `json:"scope"`
	Type     string     `json:"type"`
	Nonce    []byte     `json:"nonce"`
	Ciphered []byte     `json:"ciphered"`
	Revoked  bool       `json:"revoked"`
}

// NewFile opens (creating if needed) a file store at dir. masterKeyHex must be
// 64 hex characters (32 bytes), e.g. from `openssl rand -hex 32`.
func NewFile(dir, masterKeyHex string) (*File, error) {
	key, err := hex.DecodeString(strings.TrimSpace(masterKeyHex))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("secret store master key must be 64 hex characters")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create secret store dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure secret store dir: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &File{dir: dir, aead: aead}, nil
}

func (f *File) path(id string) string {
	// IDs are always our own UUIDs; reject anything else so a crafted ref can
	// never traverse out of the store directory.
	return filepath.Join(f.dir, id+".secret")
}

func validID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

func (f *File) seal(scope core.Scope, material core.SecretMaterial, revoked bool) ([]byte, error) {
	nonce := make([]byte, f.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Bind the scope into the AAD so a payload copied across scopes fails to open.
	aad := []byte(scope.Workspace + "|" + scope.SiteID + "|" + scope.Provider)
	payload := filePayload{
		Scope:    scope,
		Type:     material.Type,
		Nonce:    nonce,
		Ciphered: f.aead.Seal(nil, nonce, material.Bytes, aad),
		Revoked:  revoked,
	}
	return json.Marshal(payload)
}

func (f *File) load(id string) (filePayload, error) {
	var payload filePayload
	if !validID(id) {
		return payload, fmt.Errorf("invalid credential ref")
	}
	raw, err := os.ReadFile(f.path(id))
	if err != nil {
		return payload, fmt.Errorf("credential %s is not available", id)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, fmt.Errorf("credential %s is corrupt", id)
	}
	return payload, nil
}

func (f *File) open(payload filePayload) (core.SecretMaterial, error) {
	aad := []byte(payload.Scope.Workspace + "|" + payload.Scope.SiteID + "|" + payload.Scope.Provider)
	plain, err := f.aead.Open(nil, payload.Nonce, payload.Ciphered, aad)
	if err != nil {
		return core.SecretMaterial{}, fmt.Errorf("credential cannot be decrypted")
	}
	return core.SecretMaterial{Type: payload.Type, Bytes: plain}, nil
}

func (f *File) write(id string, data []byte) error {
	tmp, err := os.CreateTemp(f.dir, ".credential-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, f.path(id))
}

func (f *File) Put(_ context.Context, scope core.Scope, material core.SecretMaterial) (core.CredentialRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.NewString()
	data, err := f.seal(scope, material, false)
	if err != nil {
		return core.CredentialRef{}, err
	}
	if err := f.write(id, data); err != nil {
		return core.CredentialRef{}, err
	}
	return core.CredentialRef{ID: id, Type: material.Type}, nil
}

func (f *File) Open(_ context.Context, scope core.Scope, ref core.CredentialRef, _ core.AccessPurpose) (core.CredentialHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	payload, err := f.load(ref.ID)
	if err != nil {
		return nil, err
	}
	if payload.Revoked {
		return nil, fmt.Errorf("credential %s is revoked", ref.ID)
	}
	if payload.Scope != scope {
		return nil, fmt.Errorf("credential scope does not match")
	}
	material, err := f.open(payload)
	if err != nil {
		return nil, err
	}
	return &handle{material: material}, nil
}

func (f *File) Rotate(_ context.Context, scope core.Scope, ref core.CredentialRef, replacement core.SecretMaterial) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	payload, err := f.load(ref.ID)
	if err != nil {
		return err
	}
	if payload.Revoked {
		return fmt.Errorf("credential %s is revoked", ref.ID)
	}
	if payload.Scope != scope {
		return fmt.Errorf("credential scope does not match")
	}
	data, err := f.seal(payload.Scope, replacement, false)
	if err != nil {
		return err
	}
	return f.write(ref.ID, data)
}

func (f *File) Revoke(_ context.Context, scope core.Scope, ref core.CredentialRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	payload, err := f.load(ref.ID)
	if err != nil {
		return nil // already gone: revoke is idempotent
	}
	if payload.Scope != scope {
		return fmt.Errorf("credential scope does not match")
	}
	// Tombstone: keep scope and type, drop the material entirely.
	data, err := f.seal(payload.Scope, core.SecretMaterial{Type: payload.Type}, true)
	if err != nil {
		return err
	}
	return f.write(ref.ID, data)
}
