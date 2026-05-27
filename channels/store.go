package channels

import "time"

// Store is the persistence interface for channel records. Two
// implementations ship: an in-memory store (single-node) and a Redis-
// backed store that lets multiple Parsec nodes share the same channel
// registry.
//
// Implementations MUST be safe for concurrent use. All mutating methods
// return the event(s) that the Manager should fan out to subscribers; the
// Store does NOT do its own emit — that's the Manager's job.
type Store interface {
	// Get returns a snapshot. Deleted channels are reported as not-found
	// so callers do not need to redo the state check.
	Get(name Name) (*Channel, error)

	// List returns a snapshot of all non-deleted channel records.
	List() ([]Channel, error)

	// IsOpen reports whether the channel exists and is in StateOpen.
	IsOpen(name Name) bool

	// OpenPublic creates a new public channel, or re-opens a closed one.
	// Returns the channel snapshot and the event the caller should emit
	// (zero-value Event{} means no transition happened — e.g. already-open
	// channel had its TTL refreshed but no state change).
	OpenPublic(name Name, ttl time.Duration, now time.Time) (*Channel, Event, error)

	// CreatePrivate registers a new private channel. Fails if one already
	// exists with the same name.
	CreatePrivate(name Name, ttl time.Duration, now time.Time) (*Channel, Event, error)

	// Touch refreshes LastActive on the channel.
	Touch(name Name, now time.Time) error

	// Delete marks the channel deleted. The second return value is the
	// event the caller should emit (zero-value Event{} on a redundant
	// delete of an already-deleted channel).
	Delete(name Name, now time.Time) (Event, error)

	// Sweep runs one expiry pass and returns transition events. The Store
	// performs the writes; the Manager fans the events out.
	Sweep(now time.Time) ([]Event, error)

	// SetDelta toggles fossil-delta encoding for the channel. Used by
	// the Manager after open/create to enable the per-channel delta
	// flag without changing the OpenPublic/CreatePrivate signatures.
	SetDelta(name Name, enabled bool) error
}
