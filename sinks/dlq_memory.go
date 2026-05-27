package sinks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"

	perr "github.com/frankbardon/parsec/errors"
)

// MemoryDLQ is an in-process DLQ implementation backed by a slice + mutex.
// It is the default when Options.RedisClient is nil. Persistence is not
// promised — a process restart loses everything in the queue.
type MemoryDLQ struct {
	mu     sync.Mutex
	items  []DLQItem
	lookup RegistryLookup
}

// NewMemoryDLQ returns an empty in-process DLQ.
func NewMemoryDLQ() *MemoryDLQ { return &MemoryDLQ{} }

// SetRegistryLookup wires the Replay callback so a stored item can resolve
// back to its sink. parsec.New calls this after assembling the registry.
func (m *MemoryDLQ) SetRegistryLookup(fn RegistryLookup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookup = fn
}

// Push appends item. ID is assigned if empty.
func (m *MemoryDLQ) Push(_ context.Context, item DLQItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item.ID == "" {
		item.ID = newDLQID()
	}
	m.items = append(m.items, item)
	return nil
}

// Items returns up to limit items for sink, newest first.
func (m *MemoryDLQ) Items(_ context.Context, sink string, limit int) ([]DLQItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DLQItem, 0, len(m.items))
	for _, it := range m.items {
		if sink == "" || it.Sink == sink {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Count returns the size of the DLQ for sink.
func (m *MemoryDLQ) Count(_ context.Context, sink string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sink == "" {
		return len(m.items), nil
	}
	n := 0
	for _, it := range m.items {
		if it.Sink == sink {
			n++
		}
	}
	return n, nil
}

// Discard removes the item with id. Missing ids are no-ops.
func (m *MemoryDLQ) Discard(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, it := range m.items {
		if it.ID == id {
			m.items = append(m.items[:i], m.items[i+1:]...)
			return nil
		}
	}
	return nil
}

// Replay looks up the original sink and re-runs Send. The DLQ entry stays
// in the queue — if the replay fails through a Retrier it will land back
// here as a new item.
func (m *MemoryDLQ) Replay(ctx context.Context, id string) error {
	m.mu.Lock()
	var found DLQItem
	ok := false
	for _, it := range m.items {
		if it.ID == id {
			found = it
			ok = true
			break
		}
	}
	lookup := m.lookup
	m.mu.Unlock()
	if !ok {
		return perr.New(perr.InvalidArgument, "dlq item not found: "+id)
	}
	if lookup == nil {
		return perr.New(perr.SinkConfig, "dlq has no registry lookup configured")
	}
	sink := lookup(found.Sink)
	if sink == nil {
		return perr.New(perr.SinkConfig, "dlq replay: sink not registered: "+found.Sink)
	}
	return sink.Send(ctx, found.Recipient, found.Message)
}

// newDLQID returns a short hex string suitable for DLQItem.ID. 16 hex
// characters is 8 bytes of randomness — collisions in a single process
// are astronomically unlikely.
func newDLQID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Falling back to a sentinel is acceptable; in tests we never
		// observe rand.Read failing.
		return "00000000deadbeef"
	}
	return hex.EncodeToString(b[:])
}
