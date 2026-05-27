package sinks

import (
	"fmt"
	"sync"
)

// Factory builds a Sink instance from a YAML config block keyed by user-
// chosen name (which becomes the sink's Name()) and a raw map of the
// remaining config fields. Each built-in sink package registers a
// factory via its init() so that simply importing the package wires
// up the factory.
type Factory func(name string, raw map[string]any) (Sink, error)

var (
	factoriesMu sync.RWMutex
	factories   = make(map[string]Factory)
)

// RegisterFactory associates kind with a factory. Calling twice for the
// same kind overrides; intended for tests. Built-in sinks call this
// from their package init() so importing the sub-package is enough.
func RegisterFactory(kind string, f Factory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	factories[kind] = f
}

// LookupFactory returns the factory for kind, or false if unregistered.
func LookupFactory(kind string) (Factory, bool) {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	f, ok := factories[kind]
	return f, ok
}

// FactoryKinds returns every registered kind for diagnostic + manifest
// purposes. Order is unspecified.
func FactoryKinds() []string {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	return out
}

// BuildSink invokes the registered factory and validates the result.
// Returns a user-facing error if kind is unregistered or the factory
// rejects raw.
func BuildSink(kind, name string, raw map[string]any) (Sink, error) {
	f, ok := LookupFactory(kind)
	if !ok {
		return nil, fmt.Errorf("sinks: unknown kind %q (registered: %v)", kind, FactoryKinds())
	}
	s, err := f(name, raw)
	if err != nil {
		return nil, fmt.Errorf("sinks: build %q (kind=%s): %w", name, kind, err)
	}
	if s == nil {
		return nil, fmt.Errorf("sinks: factory for kind %q returned nil sink", kind)
	}
	return s, nil
}
