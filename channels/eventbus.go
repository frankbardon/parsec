package channels

import "context"

// EventBus is the cross-node bridge for channel lifecycle events. A
// publish on node A is delivered to every other node's local Manager so
// subscribers everywhere see the same lifecycle transitions.
//
// Implementations are responsible for tagging publications with the
// originating NodeID and skipping local re-delivery — the Manager fans
// out local events itself; the bus carries only the cross-node fan-in.
type EventBus interface {
	// Publish broadcasts ev to remote nodes. The caller will already have
	// fanned it out locally; the bus must NOT re-emit on the originating
	// node.
	Publish(ctx context.Context, ev Event) error

	// Run subscribes to the bus. onRemote fires for every remote event
	// (own publishes are filtered by NodeID). Blocks until ctx is done or
	// the underlying transport errors fatally.
	Run(ctx context.Context, onRemote func(Event)) error
}
