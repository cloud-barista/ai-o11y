package monitoring

import (
	"fmt"
	"sort"
)

// Registry holds providers by name. Looking up a name that was never
// registered is an error, so a misspelt provider in the configuration fails at
// startup instead of silently collecting nothing.
type Registry[T any] struct {
	kind  string
	items map[string]T
}

// NewRegistry returns an empty registry; kind names the seam in errors.
func NewRegistry[T any](kind string) *Registry[T] {
	return &Registry[T]{kind: kind, items: map[string]T{}}
}

// Register adds a provider. Registering the same name twice is an error so a
// provider cannot silently replace another.
func (r *Registry[T]) Register(name string, item T) error {
	if name == "" {
		return fmt.Errorf("%s: empty name", r.kind)
	}
	if _, ok := r.items[name]; ok {
		return fmt.Errorf("%s %q already registered", r.kind, name)
	}
	r.items[name] = item
	return nil
}

// Get returns the provider registered under name.
func (r *Registry[T]) Get(name string) (T, error) {
	item, ok := r.items[name]
	if !ok {
		var zero T
		return zero, fmt.Errorf("unknown %s %q", r.kind, name)
	}
	return item, nil
}

// Names lists registered names in sorted order.
func (r *Registry[T]) Names() []string {
	names := make([]string, 0, len(r.items))
	for name := range r.items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
