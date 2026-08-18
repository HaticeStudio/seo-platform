// Package secretstore provides optional SecretStore implementations for
// embedded and standalone installations. Hosts may instead implement
// core.SecretStore with their existing KMS, Vault, or encrypted database.
package secretstore

import (
	"github.com/HaticeStudio/seo-platform/core"
	"github.com/HaticeStudio/seo-platform/internal/secrets"
)

// EncryptedFiles stores AES-256-GCM encrypted credentials on one machine.
// The master key must be managed separately by the host application.
type EncryptedFiles struct{ core.SecretStore }

// NewEncryptedFiles opens a filesystem store using a 64-character hex key.
func NewEncryptedFiles(dir, masterKeyHex string) (*EncryptedFiles, error) {
	store, err := secrets.NewFile(dir, masterKeyHex)
	if err != nil {
		return nil, err
	}
	return &EncryptedFiles{SecretStore: store}, nil
}

// Memory is intended only for tests and local development.
type Memory struct{ core.SecretStore }

// NewMemory returns an ephemeral in-memory secret store.
func NewMemory() *Memory { return &Memory{SecretStore: secrets.NewMemory()} }
