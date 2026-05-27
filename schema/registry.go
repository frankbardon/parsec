// Package schema is the Parsec schema registry. Channel patterns are
// registered at application startup; the registry resolves concrete
// channel names back to the pattern that owns them and exposes the
// payload schemas (per aspect) that envelopes published on those channels
// must conform to.
//
// The package ships two things:
//
//   - A pure-Go Registry library used in-process by publishers and
//     subscribers.
//   - An HTTP handler at /parsec/schemas (mounted by internal/server) that
//     serves a JSON snapshot of the registry so out-of-process clients
//     can cache schemas locally on startup.
//
// Changes to the registry (Register / Update / Deregister) are emitted on
// a parsec channel — "public:parsec.registry.schemas" by default — so
// subscribers can hot-reload without polling the HTTP endpoint.
package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ChannelPattern declares the schema contract for a family of channels.
// The Pattern is matched against incoming channel names; on a match, the
// per-aspect PayloadSchema applies.
type ChannelPattern struct {
	Pattern     string            `json:"pattern"`
	Description string            `json:"description,omitempty"`
	Version     int               `json:"version"`
	Aspects     map[string]Aspect `json:"aspects"`
	ACL         ACLPolicy         `json:"acl,omitzero"`

	compiled Pattern
}

// Aspect is one named payload contract within a channel pattern.
type Aspect struct {
	Name          string      `json:"name"`
	Description   string      `json:"description,omitempty"`
	PayloadSchema *JSONSchema `json:"payload_schema,omitempty"`
	Required      bool        `json:"required,omitempty"`
}

// ACLPolicy is metadata describing which roles or scopes may
// subscribe / publish to channels matching the pattern. The schema
// registry does NOT enforce ACL — enforcement lives in the auth package
// + token broker. This struct is documentation that travels with the
// pattern so client libraries and the admin UI can render it.
type ACLPolicy struct {
	PublishRoles   []string `json:"publish_roles,omitempty"`
	SubscribeRoles []string `json:"subscribe_roles,omitempty"`
	Notes          string   `json:"notes,omitempty"`
}

// Sentinel errors.
var (
	ErrPatternConflict   = errors.New("schema: pattern conflicts with existing registration")
	ErrPatternNotFound   = errors.New("schema: pattern not found")
	ErrNoMatch           = errors.New("schema: no pattern matches channel")
	ErrAspectNotFound    = errors.New("schema: aspect not registered for pattern")
	ErrUnsupportedAspect = errors.New("schema: unsupported aspect")
)

// Registry is the in-memory schema store. Safe for concurrent use.
//
// The interface is intentionally minimal — third-party adapters (a SQL
// store, a config-file loader) can satisfy it without depending on
// internal Parsec types.
type Registry interface {
	Register(p ChannelPattern) error
	Update(p ChannelPattern) error
	Deregister(pattern string) error
	Resolve(channel string) (ChannelPattern, map[string]string, error)
	Get(pattern string) (ChannelPattern, error)
	List() []ChannelPattern
	Subscribe() (<-chan Change, func())
}

// ChangeKind discriminates registry change events.
type ChangeKind string

const (
	ChangeRegistered   ChangeKind = "registered"
	ChangeUpdated      ChangeKind = "updated"
	ChangeDeregistered ChangeKind = "deregistered"
)

// Change is one registry update broadcast to subscribers.
type Change struct {
	Kind    ChangeKind     `json:"kind"`
	Pattern string         `json:"pattern"`
	Schema  ChannelPattern `json:"schema,omitzero"`
	At      time.Time      `json:"at"`
}

// MemoryRegistry is the default in-process Registry. It holds patterns
// in a map keyed by pattern source string and a per-pattern history of
// prior versions for schema-evolution support.
type MemoryRegistry struct {
	mu          sync.RWMutex
	patterns    map[string]ChannelPattern
	versions    map[string][]ChannelPattern // pattern -> history (newest last)
	subscribers map[int]chan Change
	nextSub     int
}

// NewMemoryRegistry constructs an empty registry.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		patterns:    map[string]ChannelPattern{},
		versions:    map[string][]ChannelPattern{},
		subscribers: map[int]chan Change{},
	}
}

// Register adds p. The pattern must compile; duplicate patterns return
// ErrPatternConflict. The first registration is version 1 unless the
// caller set Version explicitly.
func (r *MemoryRegistry) Register(p ChannelPattern) error {
	cp, err := ParsePattern(p.Pattern)
	if err != nil {
		return err
	}
	p.compiled = cp
	if p.Version == 0 {
		p.Version = 1
	}
	r.mu.Lock()
	if _, ok := r.patterns[p.Pattern]; ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrPatternConflict, p.Pattern)
	}
	r.patterns[p.Pattern] = p
	r.versions[p.Pattern] = []ChannelPattern{p}
	r.mu.Unlock()
	r.broadcast(Change{Kind: ChangeRegistered, Pattern: p.Pattern, Schema: p, At: time.Now().UTC()})
	return nil
}

// Update replaces the entry for p.Pattern with the new definition. The
// previous version is appended to history. If Version is zero on input,
// the new version is the previous version + 1.
func (r *MemoryRegistry) Update(p ChannelPattern) error {
	cp, err := ParsePattern(p.Pattern)
	if err != nil {
		return err
	}
	p.compiled = cp
	r.mu.Lock()
	prev, ok := r.patterns[p.Pattern]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrPatternNotFound, p.Pattern)
	}
	if p.Version == 0 {
		p.Version = prev.Version + 1
	}
	r.patterns[p.Pattern] = p
	r.versions[p.Pattern] = append(r.versions[p.Pattern], p)
	r.mu.Unlock()
	r.broadcast(Change{Kind: ChangeUpdated, Pattern: p.Pattern, Schema: p, At: time.Now().UTC()})
	return nil
}

// Deregister removes pattern. Returns ErrPatternNotFound if it does not
// exist. Version history is dropped.
func (r *MemoryRegistry) Deregister(pattern string) error {
	r.mu.Lock()
	if _, ok := r.patterns[pattern]; !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrPatternNotFound, pattern)
	}
	delete(r.patterns, pattern)
	delete(r.versions, pattern)
	r.mu.Unlock()
	r.broadcast(Change{Kind: ChangeDeregistered, Pattern: pattern, At: time.Now().UTC()})
	return nil
}

// Resolve finds the pattern owning channel. The returned bindings map
// reflects any {name} placeholder values extracted during match.
// Returns ErrNoMatch when no pattern matches. When multiple patterns
// match, the one with the most literal (least wildcard) tokens wins —
// most-specific match. Ties are broken by pattern string in
// lexicographic order so resolution is deterministic.
func (r *MemoryRegistry) Resolve(channel string) (ChannelPattern, map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type candidate struct {
		p        ChannelPattern
		bindings map[string]string
		specificity int
	}
	var matches []candidate
	for _, p := range r.patterns {
		b, ok := p.compiled.Match(channel)
		if !ok {
			continue
		}
		spec := patternSpecificity(p.compiled)
		matches = append(matches, candidate{p: p, bindings: b, specificity: spec})
	}
	if len(matches) == 0 {
		return ChannelPattern{}, nil, fmt.Errorf("%w: %s", ErrNoMatch, channel)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].specificity != matches[j].specificity {
			return matches[i].specificity > matches[j].specificity
		}
		return matches[i].p.Pattern < matches[j].p.Pattern
	})
	return matches[0].p, matches[0].bindings, nil
}

func patternSpecificity(p Pattern) int {
	score := 0
	for _, t := range p.tokens {
		switch t.kind {
		case tokLiteral:
			score += 4
		case tokVar:
			score += 2
		case tokSingle:
			score += 1
		case tokDouble:
			// trailing greedy match — least specific
		}
	}
	return score
}

// Get returns the current registration for pattern.
func (r *MemoryRegistry) Get(pattern string) (ChannelPattern, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.patterns[pattern]
	if !ok {
		return ChannelPattern{}, fmt.Errorf("%w: %s", ErrPatternNotFound, pattern)
	}
	return p, nil
}

// History returns every registered version of pattern, oldest first.
// Useful for resolving an explicit schema_ref carrying a version suffix.
func (r *MemoryRegistry) History(pattern string) ([]ChannelPattern, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.versions[pattern]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPatternNotFound, pattern)
	}
	out := make([]ChannelPattern, len(h))
	copy(out, h)
	return out, nil
}

// List returns every current pattern in lexicographic order.
func (r *MemoryRegistry) List() []ChannelPattern {
	r.mu.RLock()
	out := make([]ChannelPattern, 0, len(r.patterns))
	for _, p := range r.patterns {
		out = append(out, p)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// Subscribe returns a channel that receives every Change going forward
// and a cancel func that unsubscribes. Sends are non-blocking; slow
// consumers drop events. Capacity is 64; size it accordingly.
func (r *MemoryRegistry) Subscribe() (<-chan Change, func()) {
	ch := make(chan Change, 64)
	r.mu.Lock()
	id := r.nextSub
	r.nextSub++
	r.subscribers[id] = ch
	r.mu.Unlock()
	cancel := func() {
		r.mu.Lock()
		if c, ok := r.subscribers[id]; ok {
			delete(r.subscribers, id)
			close(c)
		}
		r.mu.Unlock()
	}
	return ch, cancel
}

func (r *MemoryRegistry) broadcast(c Change) {
	r.mu.RLock()
	subs := make([]chan Change, 0, len(r.subscribers))
	for _, ch := range r.subscribers {
		subs = append(subs, ch)
	}
	r.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- c:
		default:
		}
	}
}

// ResolveAspect is a convenience: resolve channel + look up the named
// aspect's payload schema. Returns ErrAspectNotFound if the aspect is
// not declared on the matched pattern. The bindings map captures any
// {name} placeholder values.
func ResolveAspect(r Registry, channel, aspect string) (*JSONSchema, ChannelPattern, map[string]string, error) {
	p, b, err := r.Resolve(channel)
	if err != nil {
		return nil, ChannelPattern{}, nil, err
	}
	a, ok := p.Aspects[aspect]
	if !ok {
		return nil, p, b, fmt.Errorf("%w: %s on %s", ErrAspectNotFound, aspect, p.Pattern)
	}
	return a.PayloadSchema, p, b, nil
}

// Snapshot is the JSON shape returned by the HTTP endpoint and the
// shape pushed on registry-change envelopes.
type Snapshot struct {
	Patterns []ChannelPattern `json:"patterns"`
	At       time.Time        `json:"at"`
}

// Marshal returns a Snapshot as deterministic JSON bytes.
func (r *MemoryRegistry) Snapshot() Snapshot {
	return Snapshot{Patterns: r.List(), At: time.Now().UTC()}
}

// LoadSnapshot bulk-loads patterns from b (the wire form Snapshot
// produces). Existing patterns are replaced. Useful for warm-restart
// scenarios where an on-disk snapshot precedes any in-process
// registrations.
func (r *MemoryRegistry) LoadSnapshot(b []byte) error {
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	r.mu.Lock()
	r.patterns = make(map[string]ChannelPattern, len(s.Patterns))
	r.versions = make(map[string][]ChannelPattern, len(s.Patterns))
	for _, p := range s.Patterns {
		cp, err := ParsePattern(p.Pattern)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		p.compiled = cp
		if p.Version == 0 {
			p.Version = 1
		}
		r.patterns[p.Pattern] = p
		r.versions[p.Pattern] = []ChannelPattern{p}
	}
	r.mu.Unlock()
	return nil
}

// ChangeChannel is the default broadcast channel for registry changes.
// Apps may override by passing a different channel name to the Publisher
// when wiring the HTTP surface; the wire shape stays the same.
const ChangeChannel = "public:parsec.registry.schemas"
