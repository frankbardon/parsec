// Package broker wraps the github.com/centrifugal/centrifuge OSS library in
// the small surface Parsec actually needs: boot a Node, publish to a named
// channel, authorize subscribes against the Parsec channel grammar, and
// expose presence + history for the management surface.
//
// Centrifuge primitives that leak through:
//   - *centrifuge.Node     — the running broker
//   - centrifuge.Publication — the on-wire envelope (data + offset + epoch)
//
// Everything else is fronted by Parsec types so the CLI and Twirp
// surfaces never type-assert on centrifuge internals.
package broker

import (
	"context"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/channels"
	"github.com/frankbardon/parsec/errors"
)

// Options configures the broker at boot. Zero values give safe defaults
// suitable for a single-node, in-memory deployment.
type Options struct {
	// HistorySize bounds how many publications are retained per channel for
	// late-subscriber replay. Default 100.
	HistorySize int
	// PublicHistoryTTL is how long public history is kept. Default 5m.
	PublicHistoryTTL time.Duration
	// PrivateHistoryTTL is the same for private channels. Default 5m.
	PrivateHistoryTTL time.Duration
	// LogHandler receives broker-level log events. Optional.
	LogHandler centrifuge.LogHandler
	// SubscribeAuthorizer is consulted on every subscribe attempt. Default
	// authorizer allows any well-formed public channel and rejects all
	// private channels (callers must provide their own authorizer for
	// private access).
	SubscribeAuthorizer SubscribeAuthorizer
	// DeltaProvider, when non-nil, lets the broker negotiate fossil-delta
	// encoding per channel at subscribe time. Returns true to enable
	// delta on that subscription.
	DeltaProvider DeltaProvider
	// RedisShards turn the broker into a Redis-backed broker shared with
	// any other Parsec node configured against the same shards. Empty
	// means in-memory single-node mode. Pre-built via centrifuge.NewRedisShard.
	RedisShards []*centrifuge.RedisShard
	// RedisBrokerPrefix overrides the default centrifuge key prefix
	// ("parsec"). Only used when RedisShards is non-empty.
	RedisBrokerPrefix string
	// OnSubscriberChange, when non-nil, is invoked once per subscribe
	// and once per unsubscribe with the channel's parsed name and the
	// signed delta (+1 / -1). Used by the metrics layer to keep a live
	// subscriber gauge by visibility without leaking channel names.
	OnSubscriberChange func(ch channels.Name, delta int)
	// OnClientRPC, when non-nil, is registered as each connected client's RPC
	// handler. It lets an embedder receive request/response calls from clients
	// (e.g. server-authoritative intents) without taking over the broker's own
	// per-client wiring. userID is the connection's subject; the returned bytes
	// are sent back as the RPC reply, and a returned error is mapped to the
	// client as an RPC error.
	OnClientRPC func(ctx context.Context, userID string, event centrifuge.RPCEvent) ([]byte, error)
}

// SubscribeAuthorizer decides whether a connection may subscribe to ch.
// Implementations typically check a JWT in event.Token or a per-channel ACL.
type SubscribeAuthorizer func(ctx context.Context, userID string, ch channels.Name, event centrifuge.SubscribeEvent) error

// SubscribeAuthorizer returns the configured authorizer. Surface code
// uses this to drive integration tests of the revocation / token /
// rate-limit chain without spinning a real client connection.
func (b *Broker) SubscribeAuthorizer() SubscribeAuthorizer { return b.opts.SubscribeAuthorizer }

// DeltaProvider reports whether ch has fossil-delta encoding enabled.
// Used at subscribe time to negotiate the AllowedDeltaTypes set; the
// broker has no opinion of its own.
type DeltaProvider func(ch channels.Name) bool

// Broker is the Parsec-flavored handle over a centrifuge Node. Construct
// with New, start with Run.
type Broker struct {
	node *centrifuge.Node
	opts Options

	mu      sync.RWMutex
	started bool
}

// New constructs a broker. The Node is created but not started.
func New(opts Options) (*Broker, error) {
	if opts.HistorySize == 0 {
		opts.HistorySize = 100
	}
	if opts.PublicHistoryTTL == 0 {
		opts.PublicHistoryTTL = 5 * time.Minute
	}
	if opts.PrivateHistoryTTL == 0 {
		opts.PrivateHistoryTTL = 5 * time.Minute
	}
	if opts.SubscribeAuthorizer == nil {
		opts.SubscribeAuthorizer = defaultSubscribeAuthorizer
	}
	cfg := centrifuge.Config{LogHandler: opts.LogHandler}
	node, err := centrifuge.New(cfg)
	if err != nil {
		return nil, errors.Wrap(errors.BrokerInternal, "centrifuge.New", err)
	}

	// Swap in Redis broker + presence manager when shards are supplied.
	// Centrifuge initializes its in-memory equivalents inside New(); we
	// override before Run() starts.
	if len(opts.RedisShards) > 0 {
		prefix := opts.RedisBrokerPrefix
		if prefix == "" {
			prefix = "parsec"
		}
		rb, err := centrifuge.NewRedisBroker(node, centrifuge.RedisBrokerConfig{
			Prefix: prefix,
			Shards: opts.RedisShards,
		})
		if err != nil {
			return nil, errors.Wrap(errors.BrokerInternal, "centrifuge.NewRedisBroker", err)
		}
		node.SetBroker(rb)
		rp, err := centrifuge.NewRedisPresenceManager(node, centrifuge.RedisPresenceManagerConfig{
			Prefix: prefix,
			Shards: opts.RedisShards,
		})
		if err != nil {
			return nil, errors.Wrap(errors.BrokerInternal, "centrifuge.NewRedisPresenceManager", err)
		}
		node.SetPresenceManager(rp)
	}

	// Anonymous connect by default; embedders that need auth set their own
	// OnConnecting after construction via Broker.Node().OnConnecting(...).
	node.OnConnecting(func(ctx context.Context, _ centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
		return centrifuge.ConnectReply{
			Credentials: &centrifuge.Credentials{UserID: ""},
		}, nil
	})

	// Per-client subscribe authorization: the channel must parse, and the
	// configured SubscribeAuthorizer must accept it.
	auth := opts.SubscribeAuthorizer
	onChange := opts.OnSubscriberChange
	deltaProvider := opts.DeltaProvider
	onRPC := opts.OnClientRPC
	node.OnConnect(func(client *centrifuge.Client) {
		if onRPC != nil {
			client.OnRPC(func(event centrifuge.RPCEvent, cb centrifuge.RPCCallback) {
				data, err := onRPC(client.Context(), client.UserID(), event)
				if err != nil {
					cb(centrifuge.RPCReply{}, err)
					return
				}
				cb(centrifuge.RPCReply{Data: data}, nil)
			})
		}
		client.OnSubscribe(func(event centrifuge.SubscribeEvent, cb centrifuge.SubscribeCallback) {
			name, err := channels.ParseName(event.Channel)
			if err != nil {
				cb(centrifuge.SubscribeReply{}, err)
				return
			}
			if err := auth(client.Context(), client.UserID(), name, event); err != nil {
				cb(centrifuge.SubscribeReply{}, err)
				return
			}
			subOpts := centrifuge.SubscribeOptions{EnablePositioning: true, EnableRecovery: true}
			if deltaProvider != nil && deltaProvider(name) {
				subOpts.AllowedDeltaTypes = []centrifuge.DeltaType{centrifuge.DeltaTypeFossil}
			}
			cb(centrifuge.SubscribeReply{Options: subOpts}, nil)
			if onChange != nil {
				onChange(name, +1)
			}
		})
		client.OnUnsubscribe(func(event centrifuge.UnsubscribeEvent) {
			if onChange == nil {
				return
			}
			name, err := channels.ParseName(event.Channel)
			if err != nil {
				return
			}
			onChange(name, -1)
		})
	})

	return &Broker{node: node, opts: opts}, nil
}

// Node returns the underlying *centrifuge.Node. The HTTP transport mount in
// internal/server needs it. Library callers should not.
func (b *Broker) Node() *centrifuge.Node { return b.node }

// Started reports whether Run has finished booting the centrifuge node.
// Useful for test harnesses that launch Run in a goroutine and need to
// gate the first Publish on broker readiness.
func (b *Broker) Started() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.started
}

// Run starts the broker. Blocks until ctx is canceled.
func (b *Broker) Run(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return errors.New(errors.BrokerInternal, "broker already running")
	}
	if err := b.node.Run(); err != nil {
		b.mu.Unlock()
		return errors.Wrap(errors.BrokerInternal, "node.Run", err)
	}
	b.started = true
	b.mu.Unlock()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return b.node.Shutdown(shutdownCtx)
}

// Publish ships data to ch with history persistence. When delta is
// true, centrifuge fans out fossil-delta patches to subscribers that
// negotiated delta encoding; non-delta subscribers still receive the
// full payload.
func (b *Broker) Publish(ctx context.Context, ch channels.Name, data []byte, delta bool) (PublishResult, error) {
	b.mu.RLock()
	started := b.started
	b.mu.RUnlock()
	if !started {
		return PublishResult{}, errors.New(errors.BrokerNotReady, "broker not running")
	}
	opts := []centrifuge.PublishOption{
		centrifuge.WithHistory(b.opts.HistorySize, b.historyTTLFor(ch)),
	}
	if delta {
		opts = append(opts, centrifuge.WithDelta(true))
	}
	res, err := b.node.Publish(ch.String(), data, opts...)
	if err != nil {
		return PublishResult{}, errors.Wrap(errors.BrokerInternal, "publish", err)
	}
	return PublishResult{Offset: res.Offset, Epoch: res.Epoch}, nil
}

// PublishResult is the Parsec-shaped publish ack.
type PublishResult struct {
	Offset uint64
	Epoch  string
}

// Presence returns the active subscriber count on ch.
func (b *Broker) Presence(ctx context.Context, ch channels.Name) (int, error) {
	res, err := b.node.Presence(ch.String())
	if err != nil {
		return 0, errors.Wrap(errors.BrokerInternal, "presence", err)
	}
	return len(res.Presence), nil
}

// UnsubscribeAll kicks every connection subscribed to ch. Used by the
// manager-event bridge when a channel is deleted or expires.
func (b *Broker) UnsubscribeAll(ch channels.Name) {
	hub := b.node.Hub()
	target := ch.String()
	for _, client := range hub.Connections() {
		if client.IsSubscribed(target) {
			client.Unsubscribe(target)
		}
	}
}

// historyTTLFor returns the configured TTL for ch.
func (b *Broker) historyTTLFor(n channels.Name) time.Duration {
	if n.IsPrivate() {
		return b.opts.PrivateHistoryTTL
	}
	return b.opts.PublicHistoryTTL
}

// defaultSubscribeAuthorizer allows any well-formed public channel and
// rejects every private channel. Embedders that need private-channel access
// must supply a SubscribeAuthorizer that validates a token / ACL.
func defaultSubscribeAuthorizer(_ context.Context, _ string, ch channels.Name, _ centrifuge.SubscribeEvent) error {
	if ch.IsPrivate() {
		return errors.New(errors.AuthDenied, "private channel requires a SubscribeAuthorizer")
	}
	return nil
}
