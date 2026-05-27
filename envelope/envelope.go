// Package envelope is the wire-format contract every Parsec subscriber and
// publisher uses. The shape is locked here; all higher-level Parsec
// components (schema registry, client library, token broker, cache,
// telemetry) depend on it.
//
// An Envelope wraps an application payload with the metadata Parsec needs
// for ordered processing, schema introspection, and causation tracking:
//
//   - Channel       — the channel this envelope was published on
//   - Sequence      — monotonic per (channel, producer) counter assigned by
//                     the publishing client (NOT the server)
//   - ProducedAt    — wall-clock production time (millisecond precision)
//   - Producer      — identity + kind (user/agent/service) of the source
//   - Aspect        — routing key within a channel; subscribers filter on it
//   - SchemaRef     — schema-registry identifier for the payload
//   - Causation     — backward reference to the parent envelope (DAG edge)
//   - Payload       — application content, schema-conformant
//
// The recommended size ceiling is MaxEnvelopeSize (64 KB). Larger payloads
// use the data_ref pattern: include a URL in the payload and have
// subscribers fetch the actual bytes over HTTP.
package envelope

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// MaxEnvelopeSize is the recommended ceiling for an encoded envelope on the
// wire. Producers that exceed this size SHOULD switch to the data_ref
// pattern (publish a small envelope whose payload carries a URL, fetch the
// payload bytes over HTTP).
const MaxEnvelopeSize = 64 * 1024

// ProducerKind discriminates humans from agents from backend services.
type ProducerKind string

const (
	ProducerUser    ProducerKind = "user"
	ProducerAgent   ProducerKind = "agent"
	ProducerService ProducerKind = "service"
)

// Valid reports whether k is one of the recognized producer kinds.
func (k ProducerKind) Valid() bool {
	switch k {
	case ProducerUser, ProducerAgent, ProducerService:
		return true
	}
	return false
}

// Producer identifies the source of an envelope.
type Producer struct {
	ID   string       `json:"id"`
	Kind ProducerKind `json:"kind"`
}

// Causation is a backward reference to the envelope that caused this one.
// The zero value (both fields empty/zero) means "this is a root event."
type Causation struct {
	ParentChannel  string `json:"parent_channel,omitempty"`
	ParentSequence int64  `json:"parent_sequence,omitempty"`
}

// IsRoot reports whether the causation reference is unset (root event).
func (c Causation) IsRoot() bool {
	return c.ParentChannel == "" && c.ParentSequence == 0
}

// Envelope is the Parsec wire envelope.
//
// All fields except ProducedAt round-trip cleanly through encoding/json.
// ProducedAt is encoded as RFC3339 with millisecond precision via custom
// MarshalJSON / UnmarshalJSON so it matches the spec's
// "2026-05-27T14:22:09.331Z" form.
type Envelope struct {
	Channel    string          `json:"channel"`
	Sequence   int64           `json:"sequence"`
	ProducedAt time.Time       `json:"produced_at"`
	Producer   Producer        `json:"producer"`
	Aspect     string          `json:"aspect"`
	SchemaRef  string          `json:"schema_ref,omitempty"`
	Causation  Causation       `json:"causation,omitzero"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// timeLayout is RFC3339 with millisecond precision and a literal Z. Matches
// the example in the upgrade spec ("2026-05-27T14:22:09.331Z").
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// MarshalJSON encodes the envelope so ProducedAt uses millisecond
// precision regardless of the underlying time.Time's resolution.
func (e Envelope) MarshalJSON() ([]byte, error) {
	type alias Envelope
	return json.Marshal(struct {
		alias
		ProducedAt string `json:"produced_at"`
	}{
		alias:      alias(e),
		ProducedAt: e.ProducedAt.UTC().Format(timeLayout),
	})
}

// UnmarshalJSON decodes the envelope. ProducedAt parses against both the
// canonical (ms precision) layout and RFC3339Nano so subscribers tolerate
// producers that emit higher-resolution timestamps.
func (e *Envelope) UnmarshalJSON(b []byte) error {
	type alias Envelope
	aux := struct {
		*alias
		ProducedAt string `json:"produced_at"`
	}{alias: (*alias)(e)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	if aux.ProducedAt == "" {
		return nil
	}
	t, perr := time.Parse(timeLayout, aux.ProducedAt)
	if perr != nil {
		t, perr = time.Parse(time.RFC3339Nano, aux.ProducedAt)
	}
	if perr != nil {
		return fmt.Errorf("envelope: invalid produced_at %q: %w", aux.ProducedAt, perr)
	}
	e.ProducedAt = t
	return nil
}

// Sentinel errors. Callers errors.Is against these.
var (
	ErrChannelRequired    = errors.New("envelope: channel required")
	ErrAspectRequired     = errors.New("envelope: aspect required")
	ErrProducerRequired   = errors.New("envelope: producer required")
	ErrProducerKindUnknown = errors.New("envelope: unknown producer kind")
	ErrSequenceNonPositive = errors.New("envelope: sequence must be positive")
	ErrProducedAtZero     = errors.New("envelope: produced_at must be set")
	ErrTooLarge           = errors.New("envelope: encoded size exceeds MaxEnvelopeSize")
)

// Validate reports whether the envelope is structurally well-formed. It
// does NOT validate the payload against a schema (that lives in the
// schema package); it only checks the envelope-level invariants.
func (e Envelope) Validate() error {
	if e.Channel == "" {
		return ErrChannelRequired
	}
	if e.Aspect == "" {
		return ErrAspectRequired
	}
	if e.Producer.ID == "" {
		return ErrProducerRequired
	}
	if !e.Producer.Kind.Valid() {
		return ErrProducerKindUnknown
	}
	if e.Sequence <= 0 {
		return ErrSequenceNonPositive
	}
	if e.ProducedAt.IsZero() {
		return ErrProducedAtZero
	}
	return nil
}

// Encode marshals the envelope to JSON and enforces the size ceiling.
// Use this at the publisher boundary; subscribers should decode with
// json.Unmarshal and call Validate.
func (e Envelope) Encode() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxEnvelopeSize {
		return nil, fmt.Errorf("%w: %d bytes > %d", ErrTooLarge, len(b), MaxEnvelopeSize)
	}
	return b, nil
}

// Decode parses an envelope from its JSON wire form. It does NOT enforce
// the size ceiling — that is a producer-side guarantee, and subscribers
// MUST tolerate receiving any size their transport delivered.
func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// DataRefPayload is the canonical shape for the data_ref aspect: a small
// envelope whose payload points to bytes the subscriber must fetch over
// HTTP. Producers SHOULD use this shape any time the payload would
// otherwise exceed MaxEnvelopeSize.
type DataRefPayload struct {
	URL         string `json:"url"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentHash string `json:"content_hash,omitempty"`
}

// AspectDataRef is the conventional aspect name for envelopes whose
// payload is a DataRefPayload.
const AspectDataRef = "data_ref"

// SequenceTracker assigns monotonic per-channel sequences for a single
// producer. It is the publishing client's source of next-sequence numbers
// and is safe for concurrent use. Sequences start at 1.
//
// Persistence: the tracker exposes Snapshot / Restore so a publishing
// client can survive a process restart without resetting to zero. The
// publishing client owns the durable storage (typically a small
// boltdb / sqlite file or a Redis key) — the tracker itself is in-memory.
type SequenceTracker struct {
	mu    sync.RWMutex
	state map[string]*int64
}

// NewSequenceTracker constructs an empty tracker.
func NewSequenceTracker() *SequenceTracker {
	return &SequenceTracker{state: make(map[string]*int64)}
}

// Next returns the next sequence number for channel and advances the
// counter. The first call for a channel returns 1.
func (t *SequenceTracker) Next(channel string) int64 {
	t.mu.RLock()
	p, ok := t.state[channel]
	t.mu.RUnlock()
	if !ok {
		t.mu.Lock()
		if p, ok = t.state[channel]; !ok {
			var v int64
			p = &v
			t.state[channel] = p
		}
		t.mu.Unlock()
	}
	return atomic.AddInt64(p, 1)
}

// Peek returns the most recently issued sequence number for channel
// without advancing the counter. Returns 0 if no sequence has been
// issued.
func (t *SequenceTracker) Peek(channel string) int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if p, ok := t.state[channel]; ok {
		return atomic.LoadInt64(p)
	}
	return 0
}

// Snapshot returns the current per-channel sequence state. The map is a
// copy; mutating it does not affect the tracker.
func (t *SequenceTracker) Snapshot() map[string]int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]int64, len(t.state))
	for k, v := range t.state {
		out[k] = atomic.LoadInt64(v)
	}
	return out
}

// Restore replaces the tracker's state with the supplied snapshot.
// Subsequent Next calls advance from the restored values.
func (t *SequenceTracker) Restore(state map[string]int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = make(map[string]*int64, len(state))
	for k, v := range state {
		t.state[k] = &v
	}
}

// GapDetector tracks the highest sequence seen per (channel, producer)
// pair and reports gaps when an out-of-order or skipped envelope arrives.
// Subscribers use it for at-least-once delivery semantics: a positive
// Gap value means messages were missed.
type GapDetector struct {
	mu   sync.Mutex
	seen map[gapKey]int64
}

type gapKey struct {
	Channel  string
	Producer string
}

// NewGapDetector constructs an empty detector.
func NewGapDetector() *GapDetector {
	return &GapDetector{seen: make(map[gapKey]int64)}
}

// Result describes what Observe found.
type Result struct {
	// Gap is the number of envelopes missed BEFORE the one just
	// observed. Zero means perfectly in-order; a positive value means
	// the subscriber should treat the (channel, producer) stream as
	// having a hole.
	Gap int64
	// Duplicate is true when the observed sequence is <= the highest
	// seen for this (channel, producer). The detector does NOT update
	// its high-water-mark in that case.
	Duplicate bool
	// First is true when this is the first envelope ever observed for
	// the (channel, producer) pair.
	First bool
}

// Observe records the (channel, producer, sequence) tuple and reports
// the delivery state. Safe for concurrent use.
func (d *GapDetector) Observe(channel, producer string, sequence int64) Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := gapKey{channel, producer}
	prev, ok := d.seen[k]
	if !ok {
		d.seen[k] = sequence
		return Result{First: true}
	}
	if sequence <= prev {
		return Result{Duplicate: true}
	}
	gap := sequence - prev - 1
	d.seen[k] = sequence
	return Result{Gap: gap}
}

// HighWater returns the highest sequence observed for the (channel,
// producer) pair, or 0 if none.
func (d *GapDetector) HighWater(channel, producer string) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.seen[gapKey{channel, producer}]
}
