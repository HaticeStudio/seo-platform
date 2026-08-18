// Package secrets ships the development-grade SecretStore implementations.
// Production deployments plug in a real store behind core.SecretStore.
package secrets

import (
	"context"
	"fmt"
	"sync"

	"github.com/HaticeStudio/seo-platform/core"
	"github.com/google/uuid"
)

// Memory is an in-process SecretStore for tests and development. It keeps
// secrets only in RAM and forgets everything on restart.
type Memory struct {
	mu      sync.Mutex
	entries map[string]memEntry
}

type memEntry struct {
	scope    core.Scope
	material core.SecretMaterial
	revoked  bool
}

func NewMemory() *Memory {
	return &Memory{entries: make(map[string]memEntry)}
}

func (m *Memory) Put(_ context.Context, scope core.Scope, material core.SecretMaterial) (core.CredentialRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := uuid.NewString()
	m.entries[id] = memEntry{scope: scope, material: cloneMaterial(material)}
	return core.CredentialRef{ID: id, Type: material.Type}, nil
}

func (m *Memory) Open(_ context.Context, scope core.Scope, ref core.CredentialRef, _ core.AccessPurpose) (core.CredentialHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[ref.ID]
	if !ok || entry.revoked {
		return nil, fmt.Errorf("credential %s is not available", ref.ID)
	}
	if entry.scope != scope {
		return nil, fmt.Errorf("credential scope does not match")
	}
	return &handle{material: cloneMaterial(entry.material)}, nil
}

func (m *Memory) Rotate(_ context.Context, scope core.Scope, ref core.CredentialRef, replacement core.SecretMaterial) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[ref.ID]
	if !ok || entry.revoked {
		return fmt.Errorf("credential %s is not available", ref.ID)
	}
	if entry.scope != scope {
		return fmt.Errorf("credential scope does not match")
	}
	entry.material = cloneMaterial(replacement)
	m.entries[ref.ID] = entry
	return nil
}

func (m *Memory) Revoke(_ context.Context, scope core.Scope, ref core.CredentialRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[ref.ID]
	if !ok {
		return nil
	}
	if entry.scope != scope {
		return fmt.Errorf("credential scope does not match")
	}
	// Drop the material immediately; keep only a tombstone.
	entry.material = core.SecretMaterial{}
	entry.revoked = true
	m.entries[ref.ID] = entry
	return nil
}

type handle struct {
	mu       sync.Mutex
	material core.SecretMaterial
	closed   bool
}

func (h *handle) Material() core.SecretMaterial {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return core.SecretMaterial{}
	}
	return h.material
}

func (h *handle) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.material.Bytes {
		h.material.Bytes[i] = 0
	}
	h.material = core.SecretMaterial{}
	h.closed = true
}

func cloneMaterial(m core.SecretMaterial) core.SecretMaterial {
	out := core.SecretMaterial{Type: m.Type}
	out.Bytes = append([]byte(nil), m.Bytes...)
	return out
}
