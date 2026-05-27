package sinks

import (
	"context"
	"time"
)

// DLQItem is one failed-delivery record. Recipient is kept as `any` because
// each sink defines its own concrete recipient shape; persistence layers
// must round-trip through JSON so RegistryLookup can dispatch back to the
// original sink during Replay.
type DLQItem struct {
	// ID is assigned by the DLQ implementation on Push.
	ID string `json:"id"`
	// Sink is the registered name of the sink that owns this item.
	Sink string `json:"sink"`
	// At is the wall-clock instant the failure was recorded.
	At time.Time `json:"at"`
	// Recipient is the typed-per-sink target. Persisted as JSON; consumers
	// that need the original Go type should call sink-specific decoders.
	Recipient Recipient `json:"recipient,omitempty"`
	// Message is the payload that failed to deliver.
	Message Message `json:"message"`
	// Attempts is how many Send calls were made before giving up.
	Attempts int `json:"attempts"`
	// LastError is the stringified final error.
	LastError string `json:"last_error"`
}

// DLQ is the persistence surface a Retrier uses when retries are exhausted
// (or when a terminal error is observed immediately). Implementations must
// be safe for concurrent use.
type DLQ interface {
	// Push records item. The implementation assigns item.ID.
	Push(ctx context.Context, item DLQItem) error
	// Items returns up to limit items for sink, newest-first.
	Items(ctx context.Context, sink string, limit int) ([]DLQItem, error)
	// Count returns the live size of the DLQ for sink. -1 means "unknown".
	Count(ctx context.Context, sink string) (int, error)
	// Discard removes id. Missing ids are not an error.
	Discard(ctx context.Context, id string) error
	// Replay re-runs id through the original sink. Backends look the sink
	// up via the registry attached at construction time. Implementations
	// MUST NOT remove the item on a successful replay — replay through a
	// Retrier may push the item back into the DLQ; the operator decides
	// what to do next.
	Replay(ctx context.Context, id string) error
}

// RegistryLookup is the function a DLQ uses to resolve a sink name back to
// its (already retry-wrapped) Sink for Replay. Implementations accept this
// via a setter so the registry doesn't have to leak into the constructor.
type RegistryLookup func(name string) Sink

// RecipientDecoder is an optional capability a Sink (or its inner sink when
// wrapped by Retrier) can implement so the Redis DLQ can rebuild the typed
// Recipient from its persisted JSON form during Replay. Sinks that store
// state purely in their Config (e.g. slack — no per-call recipient) can
// return nil from DecodeRecipient.
//
// Reference sinks email and webhook implement this; slack returns nil.
type RecipientDecoder interface {
	DecodeRecipient(raw []byte) (Recipient, error)
}
