package channels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisEventBus fans channel lifecycle events across Parsec nodes via
// Redis pub/sub. Every locally emitted event is published with the
// originating NodeID; remote subscribers re-emit it locally, skipping
// echoes of their own publishes.
type RedisEventBus struct {
	client    redis.UniversalClient
	channel   string // pub/sub channel name
	nodeID    string
	keyPrefix string
}

// NewRedisEventBus constructs a bus. nodeID identifies this Parsec
// instance; pass an empty string to auto-generate one.
func NewRedisEventBus(client redis.UniversalClient, nodeID string) *RedisEventBus {
	if nodeID == "" {
		nodeID = randomNodeID()
	}
	return &RedisEventBus{
		client:    client,
		nodeID:    nodeID,
		keyPrefix: "parsec",
		channel:   "parsec:events",
	}
}

// WithKeyPrefix sets the namespace. Pub/sub channel becomes "<prefix>:events".
func (b *RedisEventBus) WithKeyPrefix(p string) *RedisEventBus {
	if p == "" {
		p = "parsec"
	}
	b.keyPrefix = p
	b.channel = p + ":events"
	return b
}

// NodeID returns this bus's identifier.
func (b *RedisEventBus) NodeID() string { return b.nodeID }

// wireEvent is the JSON shape sent over pub/sub.
type wireEvent struct {
	NodeID string `json:"node_id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	AtUnix int64  `json:"at_unix_nano"`
}

// Publish broadcasts ev to other nodes. Local fanout is the Manager's
// responsibility — the bus only carries cross-node traffic.
func (b *RedisEventBus) Publish(ctx context.Context, ev Event) error {
	if ev.Kind == "" {
		return nil
	}
	payload, err := json.Marshal(wireEvent{
		NodeID: b.nodeID,
		Kind:   string(ev.Kind),
		Name:   ev.Name.String(),
		AtUnix: ev.At.UnixNano(),
	})
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, b.channel, payload).Err()
}

// Run subscribes to the bus. onRemote fires for every event whose NodeID
// differs from this bus's NodeID. Blocks until ctx is canceled.
func (b *RedisEventBus) Run(ctx context.Context, onRemote func(Event)) error {
	pubsub := b.client.Subscribe(ctx, b.channel)
	defer pubsub.Close()

	// Wait for SUBSCRIBE confirmation.
	if _, err := pubsub.Receive(ctx); err != nil {
		return err
	}
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			var w wireEvent
			if err := json.Unmarshal([]byte(msg.Payload), &w); err != nil {
				continue
			}
			if w.NodeID == b.nodeID {
				continue // echo
			}
			name, err := ParseName(w.Name)
			if err != nil {
				continue
			}
			onRemote(Event{
				Kind: EventKind(w.Kind),
				Name: name,
				At:   time.Unix(0, w.AtUnix),
			})
		}
	}
}

func randomNodeID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "node-" + hex.EncodeToString(b[:])
}
