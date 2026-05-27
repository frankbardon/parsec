// Package client is the envelope-aware Go client for Parsec.
//
// The client wraps a pluggable Transport (websocket via centrifuge-go,
// HTTP-SSE for probes, or any other implementation) with the convention
// layer the upgrade spec calls for: typed aspect handlers, sequence
// tracking with gap detection, schema validation, causation propagation,
// transparent reconnect-with-resume.
//
// Subscribers parse envelopes once at the transport boundary; handlers
// see typed payloads. Publishers stamp sequence + producer + causation
// automatically; application code only supplies the aspect and payload.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/frankbardon/parsec/envelope"
	"github.com/frankbardon/parsec/schema"
)

// Transport is the wire-level surface the Client sits on top of. Each
// Transport implementation handles one connection lifecycle —
// authenticate, publish, subscribe, replay-from-sequence, presence,
// history.
//
// Implementations:
//
//   - SSETransport (this package) — HTTP-based publish + SSE subscribe,
//     suitable for probes and tests
//   - WebSocketTransport (centrifuge-go) — left as a stub the consumer
//     supplies in production
type Transport interface {
	// Connect opens the underlying transport. Idempotent.
	Connect(ctx context.Context, token string) error
	// Close terminates the connection. Pending subscriptions are
	// canceled.
	Close() error
	// Publish writes one encoded envelope to channel. The Transport
	// is responsible for delivering the bytes; sequencing was already
	// stamped by the Client.
	Publish(ctx context.Context, channel string, payload []byte) error
	// Subscribe opens a subscription. The returned channel emits raw
	// envelope bytes; the Client decodes and dispatches. cancelFn
	// terminates the subscription.
	Subscribe(ctx context.Context, channel string, opts SubscribeOptions) (<-chan []byte, func(), error)
	// History returns prior envelopes for channel. Empty slice when
	// the transport does not implement history.
	History(ctx context.Context, channel string, opts HistoryOptions) ([][]byte, error)
	// Presence returns the active presence list. Empty slice when
	// the transport does not implement presence.
	Presence(ctx context.Context, channel string) ([]PresenceEntry, error)
}

// SubscribeOptions controls the wire-level subscribe call. The Client
// fills FromSequence from its persisted sequence state on reconnect.
type SubscribeOptions struct {
	FromSequence int64
	Aspects      []string
}

// HistoryOptions controls a history fetch.
type HistoryOptions struct {
	Limit  int
	Since  int64 // since sequence (exclusive)
	Until  int64 // until sequence (inclusive); 0 = unbounded
}

// PresenceEntry is one entry in the presence list.
type PresenceEntry struct {
	UserID string `json:"user_id"`
	ClientID string `json:"client_id"`
	ConnInfo json.RawMessage `json:"conn_info,omitempty"`
}

// ClientOption configures a Client at construction.
type ClientOption func(*clientConfig)

type clientConfig struct {
	Transport  Transport
	Producer   envelope.Producer
	Validator  *schema.Validator
	Sequence   *envelope.SequenceTracker
	Snapshotter SequenceSnapshotter
}

// WithTransport injects the Transport. Required.
func WithTransport(t Transport) ClientOption {
	return func(c *clientConfig) { c.Transport = t }
}

// WithProducer sets the publisher identity stamped on every outgoing
// envelope.
func WithProducer(p envelope.Producer) ClientOption {
	return func(c *clientConfig) { c.Producer = p }
}

// WithValidator wires a schema.Validator that runs on every incoming
// envelope (per the Validator.Mode policy).
func WithValidator(v *schema.Validator) ClientOption {
	return func(c *clientConfig) { c.Validator = v }
}

// WithSequenceTracker overrides the default sequence tracker. Inject a
// shared tracker when multiple Clients in one process publish to
// overlapping channels.
func WithSequenceTracker(t *envelope.SequenceTracker) ClientOption {
	return func(c *clientConfig) { c.Sequence = t }
}

// SequenceSnapshotter persists per-channel sequence state for
// crash recovery. The client snapshots after every publish; the
// snapshotter's implementation chooses the durability tier (BoltDB,
// SQLite, file, in-memory).
type SequenceSnapshotter interface {
	Save(state map[string]int64) error
	Load() (map[string]int64, error)
}

// WithSnapshotter enables sequence persistence.
func WithSnapshotter(s SequenceSnapshotter) ClientOption {
	return func(c *clientConfig) { c.Snapshotter = s }
}

// Client is the top-level envelope-aware client.
type Client struct {
	cfg     clientConfig
	conn    bool
	mu      sync.Mutex
	subs    map[string]*Subscription
}

// New constructs a Client.
func New(opts ...ClientOption) (*Client, error) {
	cfg := clientConfig{
		Producer: envelope.Producer{Kind: envelope.ProducerService},
		Sequence: envelope.NewSequenceTracker(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.Transport == nil {
		return nil, errors.New("client: Transport required")
	}
	if cfg.Producer.ID == "" {
		return nil, errors.New("client: Producer.ID required")
	}
	if !cfg.Producer.Kind.Valid() {
		return nil, errors.New("client: Producer.Kind invalid")
	}
	if cfg.Snapshotter != nil {
		if loaded, err := cfg.Snapshotter.Load(); err == nil && loaded != nil {
			cfg.Sequence.Restore(loaded)
		}
	}
	return &Client{
		cfg:  cfg,
		subs: map[string]*Subscription{},
	}, nil
}

// Connect opens the underlying transport.
func (c *Client) Connect(ctx context.Context, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn {
		return nil
	}
	if err := c.cfg.Transport.Connect(ctx, token); err != nil {
		return err
	}
	c.conn = true
	return nil
}

// Close terminates the transport and every active subscription.
func (c *Client) Close() error {
	c.mu.Lock()
	for _, s := range c.subs {
		s.cancel()
	}
	c.subs = map[string]*Subscription{}
	c.mu.Unlock()
	return c.cfg.Transport.Close()
}

// Publish stamps env with the next sequence + ProducedAt + Producer and
// sends it over the transport.
//
// Caller-supplied fields:
//
//   - Aspect       — required
//   - Payload      — payload bytes (already JSON-encoded)
//   - Causation    — optional; set by clients that want explicit DAG
//                    edges (PublishDerived on a Subscription does this
//                    automatically)
//   - SchemaRef    — optional
//
// Stamped automatically:
//
//   - Channel      — set to the supplied channel
//   - Sequence     — next from the tracker
//   - ProducedAt   — time.Now().UTC()
//   - Producer     — from the client config
func (c *Client) Publish(ctx context.Context, channel string, env envelope.Envelope) error {
	env.Channel = channel
	env.Sequence = c.cfg.Sequence.Next(channel)
	env.ProducedAt = time.Now().UTC()
	env.Producer = c.cfg.Producer
	if err := env.Validate(); err != nil {
		return err
	}
	b, err := env.Encode()
	if err != nil {
		return err
	}
	if err := c.cfg.Transport.Publish(ctx, channel, b); err != nil {
		return err
	}
	if c.cfg.Snapshotter != nil {
		_ = c.cfg.Snapshotter.Save(c.cfg.Sequence.Snapshot())
	}
	return nil
}

// SubscribeOption is the public option type for Subscribe.
type SubscribeOption func(*SubscribeOptions)

// WithFromSequence resumes from the given sequence number. Used by the
// reconnect path; explicit consumers can also supply it for catch-up.
func WithFromSequence(seq int64) SubscribeOption {
	return func(o *SubscribeOptions) { o.FromSequence = seq }
}

// WithAspectFilter restricts the subscription to envelopes whose Aspect
// is in the supplied list. The transport may push the filter down to
// the server (when supported); otherwise the client filters locally.
func WithAspectFilter(aspects ...string) SubscribeOption {
	return func(o *SubscribeOptions) { o.Aspects = aspects }
}

// Subscribe opens a Subscription on channel. The Client owns the
// reconnect loop — the application registers handlers and drives them
// to completion; the Subscription survives transport-level disconnects.
func (c *Client) Subscribe(ctx context.Context, channel string, opts ...SubscribeOption) (*Subscription, error) {
	o := SubscribeOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	subCtx, cancel := context.WithCancel(ctx)
	s := &Subscription{
		client:      c,
		channel:     channel,
		opts:        o,
		ctx:         subCtx,
		cancel:      cancel,
		gap:         envelope.NewGapDetector(),
		aspectFuncs: map[string][]func(envelope.Envelope){},
		filter:      mapFromSlice(o.Aspects),
	}
	if err := s.start(); err != nil {
		cancel()
		return nil, err
	}
	c.mu.Lock()
	c.subs[channel] = s
	c.mu.Unlock()
	return s, nil
}

// History fetches prior envelopes for channel.
func (c *Client) History(ctx context.Context, channel string, opts HistoryOptions) ([]envelope.Envelope, error) {
	raws, err := c.cfg.Transport.History(ctx, channel, opts)
	if err != nil {
		return nil, err
	}
	out := make([]envelope.Envelope, 0, len(raws))
	for _, b := range raws {
		e, derr := envelope.Decode(b)
		if derr != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Subscription is one envelope-aware subscription. Handlers are
// dispatched in registration order; OnEnvelope handlers see every
// envelope, OnAspect handlers see only envelopes matching their aspect.
type Subscription struct {
	client    *Client
	channel   string
	opts      SubscribeOptions
	ctx       context.Context
	cancel    context.CancelFunc
	gap       *envelope.GapDetector

	mu             sync.RWMutex
	onEnvelope     []func(envelope.Envelope)
	onGap          []func(channel string, gap int64)
	aspectFuncs    map[string][]func(envelope.Envelope)
	current        envelope.Envelope // last envelope dispatched — feeds PublishDerived
	currentValid   bool
	filter         map[string]bool // local aspect filter (when transport does not push filter)

	lastSeq        int64
}

// Channel returns the subscribed channel name.
func (s *Subscription) Channel() string { return s.channel }

// OnEnvelope registers a handler that receives every envelope.
func (s *Subscription) OnEnvelope(handler func(envelope.Envelope)) {
	s.mu.Lock()
	s.onEnvelope = append(s.onEnvelope, handler)
	s.mu.Unlock()
}

// OnAspect registers a handler that receives only envelopes whose
// Aspect equals the supplied name.
func (s *Subscription) OnAspect(aspect string, handler func(envelope.Envelope)) {
	s.mu.Lock()
	s.aspectFuncs[aspect] = append(s.aspectFuncs[aspect], handler)
	s.mu.Unlock()
}

// OnAspectTyped is the typed-payload variant of OnAspect. The payload
// is unmarshaled into T; on failure the handler is skipped and the
// error is silently dropped (use the Subscription's Validator to
// surface payload errors).
func OnAspectTyped[T any](s *Subscription, aspect string, handler func(T, envelope.Envelope)) {
	s.OnAspect(aspect, func(env envelope.Envelope) {
		var t T
		if err := json.Unmarshal(env.Payload, &t); err != nil {
			return
		}
		handler(t, env)
	})
}

// OnGap registers a handler that fires when a sequence gap is detected
// on the subscription. Apps typically use this to reload state from a
// durable source.
func (s *Subscription) OnGap(handler func(channel string, gap int64)) {
	s.mu.Lock()
	s.onGap = append(s.onGap, handler)
	s.mu.Unlock()
}

// History is a convenience that delegates to Client.History.
func (s *Subscription) History(ctx context.Context, opts HistoryOptions) ([]envelope.Envelope, error) {
	return s.client.History(ctx, s.channel, opts)
}

// Presence delegates to the Transport.
func (s *Subscription) Presence(ctx context.Context) ([]PresenceEntry, error) {
	return s.client.cfg.Transport.Presence(ctx, s.channel)
}

// Unsubscribe terminates the subscription.
func (s *Subscription) Unsubscribe() error {
	s.cancel()
	s.client.mu.Lock()
	delete(s.client.subs, s.channel)
	s.client.mu.Unlock()
	return nil
}

// PublishDerived publishes an envelope on s.channel whose Causation
// references the most recently dispatched envelope. This is the
// programmatic surface for the "auto-propagated causation" behavior
// the upgrade spec describes.
func (s *Subscription) PublishDerived(ctx context.Context, aspect string, payload []byte) error {
	env := envelope.Envelope{
		Aspect:  aspect,
		Payload: payload,
	}
	s.mu.RLock()
	if s.currentValid {
		env.Causation = envelope.Causation{
			ParentChannel:  s.current.Channel,
			ParentSequence: s.current.Sequence,
		}
	}
	s.mu.RUnlock()
	return s.client.Publish(ctx, s.channel, env)
}

func (s *Subscription) start() error {
	stream, cancel, err := s.client.cfg.Transport.Subscribe(s.ctx, s.channel, s.opts)
	if err != nil {
		return err
	}
	go s.run(stream, cancel)
	return nil
}

func (s *Subscription) run(stream <-chan []byte, transportCancel func()) {
	defer func() { transportCancel() }()
	for {
		select {
		case <-s.ctx.Done():
			return
		case raw, ok := <-stream:
			if !ok {
				if s.ctx.Err() != nil {
					return
				}
				transportCancel()
				s.opts.FromSequence = s.lastSeq
				newStream, newCancel, err := s.client.cfg.Transport.Subscribe(s.ctx, s.channel, s.opts)
				if err != nil {
					return
				}
				stream = newStream
				transportCancel = newCancel
				continue
			}
			s.dispatch(raw)
		}
	}
}

func (s *Subscription) dispatch(raw []byte) {
	env, err := envelope.Decode(raw)
	if err != nil {
		return
	}
	if s.filter != nil && !s.filter[env.Aspect] {
		return
	}
	if s.client.cfg.Validator != nil {
		if err := s.client.cfg.Validator.Check(env); err != nil {
			return
		}
	}
	res := s.gap.Observe(env.Channel, env.Producer.ID, env.Sequence)
	if res.Duplicate {
		return
	}
	s.lastSeq = env.Sequence
	s.mu.Lock()
	s.current = env
	s.currentValid = true
	type envFn = func(envelope.Envelope)
	type gapFn = func(string, int64)
	onEnv := append([]envFn{}, s.onEnvelope...)
	aspectFns := append([]envFn{}, s.aspectFuncs[env.Aspect]...)
	gapFns := append([]gapFn{}, s.onGap...)
	s.mu.Unlock()
	if res.Gap > 0 {
		for _, fn := range gapFns {
			fn(env.Channel, res.Gap)
		}
	}
	for _, fn := range onEnv {
		fn(env)
	}
	for _, fn := range aspectFns {
		fn(env)
	}
}

func mapFromSlice(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

// ---------- FileSnapshotter ----------

// FileSnapshotter persists the sequence tracker state to a JSON file.
// Suitable for single-process clients that need to survive a restart
// without losing per-channel sequence counters.
type FileSnapshotter struct {
	Path string
	io   FileIO // optional; nil = OS filesystem
}

// FileIO is the minimal filesystem surface FileSnapshotter consumes.
// Tests substitute an in-memory implementation.
type FileIO interface {
	Read(path string) ([]byte, error)
	Write(path string, data []byte) error
}

// NewFileSnapshotter constructs a snapshotter writing to path.
func NewFileSnapshotter(path string) *FileSnapshotter {
	return &FileSnapshotter{Path: path, io: osFileIO{}}
}

// WithFileIO swaps the underlying IO (tests).
func (f *FileSnapshotter) WithFileIO(io FileIO) *FileSnapshotter {
	f.io = io
	return f
}

// Save writes state.
func (f *FileSnapshotter) Save(state map[string]int64) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return f.io.Write(f.Path, b)
}

// Load reads state.
func (f *FileSnapshotter) Load() (map[string]int64, error) {
	b, err := f.io.Read(f.Path)
	if err != nil {
		return nil, err
	}
	var out map[string]int64
	if uerr := json.Unmarshal(b, &out); uerr != nil {
		return nil, uerr
	}
	return out, nil
}

type osFileIO struct{}

func (osFileIO) Read(path string) ([]byte, error) {
	return readFile(path)
}

func (osFileIO) Write(path string, data []byte) error {
	return writeFile(path, data)
}

// readFile / writeFile are package-level indirections so the snapshotter
// stays test-friendly without importing os into the client surface
// directly. They are wired below.
var (
	readFile  = func(path string) ([]byte, error) { return nil, fmt.Errorf("client: file IO not wired") }
	writeFile = func(path string, data []byte) error { return fmt.Errorf("client: file IO not wired") }
)
