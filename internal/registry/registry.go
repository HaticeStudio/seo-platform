// Package registry holds the set of installed providers. Providers register
// at wiring time; nothing about an uninstalled provider leaks into
// configuration or the API.
package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/HaticeStudio/seo-platform/core"
)

type Registry struct {
	mu        sync.RWMutex
	providers map[string]core.Provider
}

func New() *Registry {
	return &Registry{providers: make(map[string]core.Provider)}
}

// Register adds a provider under its descriptor name. Registering the same
// name twice is a wiring bug and fails loudly.
func (r *Registry) Register(p core.Provider) error {
	name := p.Descriptor().Name
	if name == "" {
		return fmt.Errorf("provider descriptor has empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider %q registered twice", name)
	}
	r.providers[name] = p
	return nil
}

func (r *Registry) Get(name string) (core.Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// Descriptors returns installed provider descriptors sorted by name, which is
// what the API exposes for the Console to render from.
func (r *Registry) Descriptors() []core.Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.Descriptor, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
