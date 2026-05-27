package channels

import "time"

// EventKind is the lifecycle transition a Channel just made.
type EventKind string

const (
	// EventOpened fires on first open or re-open of a public channel.
	EventOpened EventKind = "opened"
	// EventClosed fires when a public channel transitions to StateClosed via
	// Sweep (TTL exceeded). Public-close semantics: no new subscribers, but
	// existing subscribers are not kicked.
	EventClosed EventKind = "closed"
	// EventDeleted fires when a channel is removed — either by an explicit
	// Delete call or because a private channel expired and was reaped from
	// the manager. Subscribers MUST be kicked.
	EventDeleted EventKind = "deleted"
)

// Event is the payload emitted on every lifecycle transition.
type Event struct {
	Kind EventKind
	Name Name
	At   time.Time
}
