package sinks

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	perr "github.com/frankbardon/parsec/errors"
)

// RedisDLQ persists DLQ items as Redis Stream entries. One stream per sink,
// keyed by `<prefix>:dlq:<sink>`. IDs are the native stream IDs so that
// Discard / Replay can route directly via XRANGE / XDEL.
//
// Operators tune retention via MaxLen (XADD MAXLEN ~).
type RedisDLQ struct {
	client    redis.UniversalClient
	keyPrefix string
	maxLen    int64

	mu     sync.RWMutex
	lookup RegistryLookup

	// streamsMu guards knownStreams. We track the set of stream keys we
	// have created so the "list across all sinks" path doesn't need a
	// KEYS scan.
	streamsMu    sync.Mutex
	knownStreams map[string]struct{}
}

// NewRedisDLQ constructs a Redis-backed DLQ. The default key prefix is
// "parsec" and the default MAXLEN cap is 10000 entries per sink stream.
func NewRedisDLQ(client redis.UniversalClient) *RedisDLQ {
	return &RedisDLQ{
		client:       client,
		keyPrefix:    "parsec",
		maxLen:       10000,
		knownStreams: make(map[string]struct{}),
	}
}

// WithKeyPrefix changes the Redis namespace.
func (d *RedisDLQ) WithKeyPrefix(p string) *RedisDLQ {
	if p == "" {
		p = "parsec"
	}
	d.keyPrefix = p
	return d
}

// WithMaxLen overrides the per-stream MAXLEN cap. <=0 disables capping.
func (d *RedisDLQ) WithMaxLen(n int64) *RedisDLQ {
	d.maxLen = n
	return d
}

// SetRegistryLookup attaches the Sink lookup used by Replay.
func (d *RedisDLQ) SetRegistryLookup(fn RegistryLookup) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lookup = fn
}

func (d *RedisDLQ) streamKey(sink string) string {
	return d.keyPrefix + ":dlq:" + sink
}

// rememberStream marks a stream key as known so future global queries can
// enumerate it without using KEYS.
func (d *RedisDLQ) rememberStream(key string) {
	d.streamsMu.Lock()
	d.knownStreams[key] = struct{}{}
	d.streamsMu.Unlock()
}

// Push XADDs the encoded item. The returned ID is the Redis stream ID.
func (d *RedisDLQ) Push(ctx context.Context, item DLQItem) error {
	if item.Sink == "" {
		return perr.New(perr.InvalidArgument, "dlq push: sink name required")
	}
	if item.At.IsZero() {
		item.At = time.Now().UTC()
	}
	payload, err := encodeDLQItem(item)
	if err != nil {
		return perr.Wrap(perr.Internal, "encode dlq item", err)
	}
	args := &redis.XAddArgs{
		Stream: d.streamKey(item.Sink),
		Values: map[string]interface{}{"item": payload},
	}
	if d.maxLen > 0 {
		args.MaxLen = d.maxLen
		args.Approx = true
	}
	id, err := d.client.XAdd(ctx, args).Result()
	if err != nil {
		return perr.Wrap(perr.SinkUnavailable, "xadd dlq", err)
	}
	d.rememberStream(d.streamKey(item.Sink))
	_ = id
	return nil
}

// Items returns up to limit newest-first DLQItems for sink. If sink is
// empty, items across every known stream are merged.
func (d *RedisDLQ) Items(ctx context.Context, sink string, limit int) ([]DLQItem, error) {
	streams := d.streamsFor(sink)
	out := make([]DLQItem, 0)
	for _, key := range streams {
		// XREVRANGE returns newest first.
		entries, err := d.client.XRevRangeN(ctx, key, "+", "-", int64(maxFetch(limit))).Result()
		if err != nil {
			return nil, perr.Wrap(perr.SinkUnavailable, "xrevrange dlq", err)
		}
		for _, e := range entries {
			it, err := decodeStreamEntry(e)
			if err != nil {
				continue
			}
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Count returns XLEN for sink, or the sum across all known streams when
// sink is empty.
func (d *RedisDLQ) Count(ctx context.Context, sink string) (int, error) {
	streams := d.streamsFor(sink)
	var total int64
	for _, key := range streams {
		n, err := d.client.XLen(ctx, key).Result()
		if err != nil {
			// If the stream doesn't exist XLen returns 0 and a nil error
			// in recent go-redis; defensively treat redis.Nil as zero.
			if err == redis.Nil {
				continue
			}
			return 0, perr.Wrap(perr.SinkUnavailable, "xlen dlq", err)
		}
		total += n
	}
	return int(total), nil
}

// Discard removes id from its stream. We resolve the stream by scanning
// every known sink stream — Redis IDs encode `<ms>-<seq>` and do not
// embed the sink, so a per-sink lookup is unavoidable. Memory cost is
// O(#known sinks), which is small in practice.
func (d *RedisDLQ) Discard(ctx context.Context, id string) error {
	for _, key := range d.allStreams() {
		n, err := d.client.XDel(ctx, key, id).Result()
		if err != nil {
			return perr.Wrap(perr.SinkUnavailable, "xdel dlq", err)
		}
		if n > 0 {
			return nil
		}
	}
	return nil
}

// Replay fetches id, locates the owning sink, and re-sends. The original
// stream entry is not removed — replayed items that fail again will land
// back in the DLQ via the Retrier.
func (d *RedisDLQ) Replay(ctx context.Context, id string) error {
	for _, key := range d.allStreams() {
		entries, err := d.client.XRange(ctx, key, id, id).Result()
		if err != nil {
			return perr.Wrap(perr.SinkUnavailable, "xrange dlq", err)
		}
		if len(entries) == 0 {
			continue
		}
		it, err := decodeStreamEntry(entries[0])
		if err != nil {
			return perr.Wrap(perr.Internal, "decode dlq item", err)
		}
		d.mu.RLock()
		lookup := d.lookup
		d.mu.RUnlock()
		if lookup == nil {
			return perr.New(perr.SinkConfig, "dlq has no registry lookup configured")
		}
		sink := lookup(it.Sink)
		if sink == nil {
			return perr.New(perr.SinkConfig, "dlq replay: sink not registered: "+it.Sink)
		}
		// Decode the recipient back into its typed form if the sink
		// supports it. Otherwise pass the raw JSON through.
		recipient := it.Recipient
		if raw, ok := recipient.(json.RawMessage); ok {
			if dec, ok := unwrapToDecoder(sink); ok {
				typed, err := dec.DecodeRecipient(raw)
				if err != nil {
					return perr.Wrap(perr.Internal, "dlq replay decode recipient", err)
				}
				recipient = typed
			}
		}
		return sink.Send(ctx, recipient, it.Message)
	}
	return perr.New(perr.InvalidArgument, "dlq item not found: "+id)
}

// unwrapToDecoder peels off Retrier wrappers to find a RecipientDecoder.
func unwrapToDecoder(s Sink) (RecipientDecoder, bool) {
	for s != nil {
		if dec, ok := s.(RecipientDecoder); ok {
			return dec, true
		}
		if u, ok := s.(interface{ Unwrap() Sink }); ok {
			s = u.Unwrap()
			continue
		}
		return nil, false
	}
	return nil, false
}

// streamsFor returns the stream key(s) covering sink. Empty sink returns
// every known stream.
func (d *RedisDLQ) streamsFor(sink string) []string {
	if sink != "" {
		key := d.streamKey(sink)
		d.rememberStream(key)
		return []string{key}
	}
	return d.allStreams()
}

func (d *RedisDLQ) allStreams() []string {
	d.streamsMu.Lock()
	defer d.streamsMu.Unlock()
	out := make([]string, 0, len(d.knownStreams))
	for k := range d.knownStreams {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// maxFetch caps the per-stream XREVRANGE count. limit==0 means "no cap",
// which we widen to 1000 to keep memory bounded on global listings.
func maxFetch(limit int) int {
	if limit <= 0 {
		return 1000
	}
	return limit
}

// streamItemEnvelope is the JSON shape stored in the stream's "item"
// field. We persist Recipient as raw JSON so the sink-specific concrete
// type round-trips back through json.Unmarshal in the sink's Send.
type streamItemEnvelope struct {
	Sink      string          `json:"sink"`
	At        time.Time       `json:"at"`
	Recipient json.RawMessage `json:"recipient,omitempty"`
	Message   Message         `json:"message"`
	Attempts  int             `json:"attempts"`
	LastError string          `json:"last_error,omitempty"`
}

func encodeDLQItem(it DLQItem) ([]byte, error) {
	env := streamItemEnvelope{
		Sink:      it.Sink,
		At:        it.At,
		Message:   it.Message,
		Attempts:  it.Attempts,
		LastError: it.LastError,
	}
	if it.Recipient != nil {
		raw, err := json.Marshal(it.Recipient)
		if err != nil {
			return nil, err
		}
		env.Recipient = raw
	}
	return json.Marshal(env)
}

func decodeStreamEntry(e redis.XMessage) (DLQItem, error) {
	raw, _ := e.Values["item"].(string)
	if raw == "" {
		return DLQItem{}, perr.New(perr.Internal, "dlq stream entry missing item field")
	}
	var env streamItemEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return DLQItem{}, err
	}
	out := DLQItem{
		ID:        e.ID,
		Sink:      env.Sink,
		At:        env.At,
		Message:   env.Message,
		Attempts:  env.Attempts,
		LastError: env.LastError,
	}
	if len(env.Recipient) > 0 {
		// Preserve the raw JSON so sink-specific code can decode it later.
		// Most callers (list / count) only display the metadata; Replay
		// decodes through the sink's typed assertion.
		out.Recipient = env.Recipient
	}
	return out, nil
}
