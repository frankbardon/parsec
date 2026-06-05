package schema

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
)

// PublishFunc is the broker-side primitive the Publisher calls for
// every Change. Mirrors the shape of parsec.Parsec.Publish minus the
// PublishResult: the broadcaster does not care about delivery metadata.
type PublishFunc func(ctx context.Context, channel string, data []byte) error

// PublisherOptions configures the broadcast loop.
type PublisherOptions struct {
	// Channel is the parsec channel name onto which Change events are
	// pushed. When empty, ChangeChannel is used.
	Channel string

	// Logger receives WARN entries for marshal / publish failures.
	// nil uses slog.Default.
	Logger *slog.Logger

	// EnsureChannel, when non-nil, is invoked once at Run start with
	// the resolved channel name. Use this to OpenPublic the channel
	// on the broker before publication. Returning an error aborts
	// Run before the subscribe loop starts.
	EnsureChannel func(channel string) error
}

// Publisher bridges a schema Registry to a parsec channel. Each
// registry Change (Register / Update / Deregister) is JSON-marshaled
// and published on the configured channel so subscribers can
// hot-reload without polling the HTTP snapshot endpoint.
//
// The wire shape of each publication is exactly schema.Change as
// emitted by the registry; future revisions may wrap it in an
// envelope, at which point the format_version of the descriptor will
// be bumped.
type Publisher struct {
	reg     Registry
	publish PublishFunc
	channel string
	logger  *slog.Logger
	ensure  func(string) error
}

// NewPublisher constructs a Publisher. The Registry and PublishFunc
// are required; nil for either is a programmer error and panics so
// the misuse surfaces at boot, not in production.
func NewPublisher(reg Registry, publish PublishFunc, opts PublisherOptions) *Publisher {
	if reg == nil {
		panic("schema: NewPublisher: nil Registry")
	}
	if publish == nil {
		panic("schema: NewPublisher: nil PublishFunc")
	}
	ch := opts.Channel
	if ch == "" {
		ch = ChangeChannel
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{
		reg:     reg,
		publish: publish,
		channel: ch,
		logger:  logger,
		ensure:  opts.EnsureChannel,
	}
}

// Channel returns the resolved broadcast channel name.
func (p *Publisher) Channel() string { return p.channel }

// Run subscribes to the Registry and republishes every Change until
// ctx is done. Returns nil on graceful shutdown (ctx.Done received or
// the underlying subscriber channel closed); returns the EnsureChannel
// error when the pre-flight hook fails.
//
// Marshal / publish errors are logged at WARN and the loop continues:
// schema broadcasts are best-effort, and a transient broker failure
// must not block the registry. Subscribers re-sync via the HTTP
// snapshot endpoint when they detect a gap.
func (p *Publisher) Run(ctx context.Context) error {
	if p.ensure != nil {
		if err := p.ensure(p.channel); err != nil {
			return err
		}
	}
	events, cancel := p.reg.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		case change, ok := <-events:
			if !ok {
				return nil
			}
			body, err := json.Marshal(change)
			if err != nil {
				p.logger.Warn("schema: broadcast marshal failed",
					"channel", p.channel, "pattern", change.Pattern, "err", err)
				continue
			}
			if err := p.publish(ctx, p.channel, body); err != nil {
				p.logger.Warn("schema: broadcast publish failed",
					"channel", p.channel, "pattern", change.Pattern, "kind", change.Kind, "err", err)
			}
		}
	}
}
