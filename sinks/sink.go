// Package sinks defines the small Sink interface that publishers can use
// when a realtime channel has no live subscriber and a message must be
// delivered out-of-band. Parsec ships three reference sinks (email, slack,
// webhook); embedders can register their own.
//
// Sinks are intentionally minimal — they accept a typed Message and a
// Recipient and either succeed or return a coded error. Backpressure,
// retry, and queueing are the embedder's responsibility.
package sinks

import "context"

// Message is what a sink delivers. Body is interpreted by the sink (HTML
// for email, mrkdwn for slack, JSON for webhook).
type Message struct {
	Subject string
	Body    string
	// Metadata is attached verbatim by sinks that support it (e.g. slack
	// blocks, webhook headers, email X-* headers).
	Metadata map[string]string
}

// Recipient is the per-call target. The shape varies per sink — see each
// sink package for the concrete type it expects.
type Recipient any

// Sink delivers a Message to a Recipient. Implementations must be safe for
// concurrent use.
type Sink interface {
	// Name is a short identifier used by the registry and the manifest.
	Name() string
	// Send delivers the message. Returns a coded error on failure.
	Send(ctx context.Context, to Recipient, m Message) error
}

// Registry is a name-to-Sink lookup table. Embedders register Sinks at boot
// and publishers resolve them by name.
type Registry struct {
	sinks map[string]Sink
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{sinks: make(map[string]Sink)}
}

// Register adds s under its declared name. Last write wins.
func (r *Registry) Register(s Sink) { r.sinks[s.Name()] = s }

// Get returns the sink registered as name, or nil if absent.
func (r *Registry) Get(name string) Sink { return r.sinks[name] }

// Names returns the registered names in arbitrary order. Used by the manifest.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.sinks))
	for k := range r.sinks {
		out = append(out, k)
	}
	return out
}
