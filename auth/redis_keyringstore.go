package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrKeyRingConflict reports that the persisted ring moved on since the
// caller loaded it, so saving would silently drop somebody else's
// rotation. Callers reload and re-apply their mutation.
//
// Deliberately a sentinel rather than a coded PARSEC_* error: it never
// crosses an RPC boundary, because the retry happens inside the process
// that hit the conflict.
var ErrKeyRingConflict = errors.New("auth: keyring changed since it was loaded")

// versionUnknown marks a store that has not read the version counter yet.
// Save is unconditional in that state — there is no known baseline to
// compare against, which matches "first write wins" for a ring the caller
// never loaded.
const versionUnknown = int64(-1)

// defaultReconcileInterval is how often Watch re-reads the ring when the
// caller does not choose. Redis pub/sub is at-most-once: a node that is
// disconnected when a rotation is published never sees that event, so the
// periodic read is what bounds how long a node can serve a stale ring.
const defaultReconcileInterval = 30 * time.Second

// maxSaveAttempts bounds the optimistic-concurrency retry loop.
const maxSaveAttempts = 5

// RedisKeyRingStore persists the KeyRing as a JSON blob in Redis with a
// monotonic version counter. Save is a compare-and-set against the version
// the store last read, so two nodes rotating at once cannot lose one
// another's changes. Watch combines pub/sub fanout with a periodic
// reconcile so a missed event self-heals.
type RedisKeyRingStore struct {
	client    redis.UniversalClient
	keyPrefix string

	// lastVersion is the version counter as of the most recent Load or
	// Save. Save refuses to write when the stored counter has moved past
	// it. versionUnknown until the first read.
	lastVersion atomic.Int64

	reconcileEvery time.Duration
	onError        func(error)
}

// NewRedisKeyRingStore constructs a store backed by client. The default
// key prefix is "parsec"; the layout becomes:
//
//	<prefix>:keyring          string (JSON snapshot)
//	<prefix>:keyring:version  integer (monotonic)
//	<prefix>:keyring:events   pub/sub channel (version-number payloads)
func NewRedisKeyRingStore(client redis.UniversalClient) *RedisKeyRingStore {
	s := &RedisKeyRingStore{client: client, keyPrefix: "parsec"}
	s.lastVersion.Store(versionUnknown)
	return s
}

// WithKeyPrefix overrides the namespace.
func (s *RedisKeyRingStore) WithKeyPrefix(p string) *RedisKeyRingStore {
	if p == "" {
		p = "parsec"
	}
	s.keyPrefix = p
	return s
}

// WithReconcileInterval sets how often Watch re-reads the ring as a
// backstop for dropped pub/sub events. Zero keeps the default; negative
// disables the reconcile and leaves Watch dependent on pub/sub alone.
func (s *RedisKeyRingStore) WithReconcileInterval(d time.Duration) *RedisKeyRingStore {
	s.reconcileEvery = d
	return s
}

// WithErrorHook installs a callback for non-fatal Watch errors — a failed
// reconcile read, a dropped subscription. Watch keeps running; the hook
// exists so callers can count them.
func (s *RedisKeyRingStore) WithErrorHook(f func(error)) *RedisKeyRingStore {
	s.onError = f
	return s
}

func (s *RedisKeyRingStore) keyringKey() string { return s.keyPrefix + ":keyring" }
func (s *RedisKeyRingStore) versionKey() string { return s.keyPrefix + ":keyring:version" }
func (s *RedisKeyRingStore) eventChan() string  { return s.keyPrefix + ":keyring:events" }

// Version returns the version counter as of the last Load or Save, or -1
// when the store has not read it yet. Exposed so operators can see, per
// node, which revision of the ring that node is serving: nodes that
// disagree report different numbers.
func (s *RedisKeyRingStore) Version() int64 { return s.lastVersion.Load() }

func (s *RedisKeyRingStore) reportError(err error) {
	if s.onError != nil && err != nil {
		s.onError(err)
	}
}

// Load reads the persisted snapshot and records the version it belongs to.
// Returns (nil, os.ErrNotExist) when the key does not exist so the caller
// can bootstrap.
//
// Body and version are written together inside one MULTI, so reading the
// version on both sides of the body read and retrying on a change yields a
// matched pair without holding a transaction open.
func (s *RedisKeyRingStore) Load(ctx context.Context) (*KeyRing, error) {
	for attempt := 0; attempt < maxSaveAttempts; attempt++ {
		before, err := s.readVersion(ctx)
		if err != nil {
			return nil, err
		}
		body, err := s.client.Get(ctx, s.keyringKey()).Result()
		if errors.Is(err, redis.Nil) {
			// Record the version anyway: it makes the bootstrap Save in
			// Ensure a compare-and-set against "still empty", so two nodes
			// starting against a fresh Redis cannot both mint a ring and
			// have the loser serve keys that were overwritten.
			s.lastVersion.Store(before)
			return nil, os.ErrNotExist
		}
		if err != nil {
			return nil, fmt.Errorf("redis Get keyring: %w", err)
		}
		after, err := s.readVersion(ctx)
		if err != nil {
			return nil, err
		}
		if before != after {
			// A write landed between the two reads; the body we hold may
			// belong to either version. Read again.
			continue
		}
		ring, err := decodeKeyRing([]byte(body))
		if err != nil {
			return nil, err
		}
		s.lastVersion.Store(after)
		return ring, nil
	}
	return nil, fmt.Errorf("load keyring: version kept changing after %d attempts", maxSaveAttempts)
}

// readVersion returns the current counter, treating a missing key as 0.
func (s *RedisKeyRingStore) readVersion(ctx context.Context) (int64, error) {
	v, err := s.client.Get(ctx, s.versionKey()).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("redis Get keyring version: %w", err)
	}
	return v, nil
}

func decodeKeyRing(body []byte) (*KeyRing, error) {
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("decode keyring: %w", err)
	}
	if !supportedFormatVersion(snap.FormatVersion) {
		return nil, fmt.Errorf("keyring format_version %q is not supported", snap.FormatVersion)
	}
	r := NewKeyRing()
	if err := r.LoadSnapshot(snap); err != nil {
		return nil, err
	}
	return r, nil
}

// Save persists ring, but only if the stored version still matches the one
// this store last read. On a mismatch it writes nothing and returns
// ErrKeyRingConflict: the ring being saved was derived from an older
// snapshot, and writing the whole blob would drop whatever landed in
// between.
//
// The WATCH still guards the read-modify-write window itself, so a writer
// that never loaded (version unknown) is serialized rather than
// interleaved.
func (s *RedisKeyRingStore) Save(ctx context.Context, r *KeyRing) error {
	snap := r.Snapshot()
	snap.FormatVersion = keyringFormatVersion
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	expected := s.lastVersion.Load()
	var written int64
	for range maxSaveAttempts {
		err = s.client.Watch(ctx, func(tx *redis.Tx) error {
			current, verr := tx.Get(ctx, s.versionKey()).Int64()
			if verr != nil && !errors.Is(verr, redis.Nil) {
				return verr
			}
			if errors.Is(verr, redis.Nil) {
				current = 0
			}
			if expected != versionUnknown && current != expected {
				return ErrKeyRingConflict
			}
			written = current + 1
			_, perr := tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				p.Set(ctx, s.keyringKey(), body, 0)
				p.Set(ctx, s.versionKey(), written, 0)
				p.Publish(ctx, s.eventChan(), written)
				return nil
			})
			return perr
		}, s.versionKey())
		switch {
		case err == nil:
			s.lastVersion.Store(written)
			return nil
		case errors.Is(err, ErrKeyRingConflict):
			return err
		case errors.Is(err, redis.TxFailedErr):
			// Someone wrote inside our transaction window. Retry; the
			// version check above decides whether that write conflicts.
			continue
		default:
			return fmt.Errorf("save keyring: %w", err)
		}
	}
	return fmt.Errorf("save keyring: contention after %d attempts", maxSaveAttempts)
}

// Watch keeps the caller's ring current. It subscribes for rotation
// fanout and, because pub/sub delivers at most once, also re-reads the
// ring periodically so a node that missed an event converges anyway.
//
// It returns only when ctx is canceled: a dropped subscription is
// reconnected with backoff rather than surfaced, matching
// FileKeyRingStore.Watch. onChange fires only when the version actually
// advanced, so a node's own writes do not trigger a self-reload.
func (s *RedisKeyRingStore) Watch(ctx context.Context, onChange func(*KeyRing)) error {
	var notified atomic.Int64
	notified.Store(s.lastVersion.Load())

	// deliver reloads the ring and calls onChange if this is news.
	deliver := func() {
		ring, err := s.Load(ctx)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && ctx.Err() == nil {
				s.reportError(fmt.Errorf("keyring reconcile: %w", err))
			}
			return
		}
		v := s.lastVersion.Load()
		for {
			seen := notified.Load()
			if v <= seen {
				return
			}
			if notified.CompareAndSwap(seen, v) {
				break
			}
		}
		onChange(ring)
	}

	var wg sync.WaitGroup
	if interval := s.reconcileInterval(); interval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					deliver()
				}
			}
		}()
	}

	s.subscribeLoop(ctx, deliver)
	wg.Wait()
	return nil
}

func (s *RedisKeyRingStore) reconcileInterval() time.Duration {
	if s.reconcileEvery == 0 {
		return defaultReconcileInterval
	}
	return s.reconcileEvery
}

// subscribeLoop keeps a subscription alive for the life of ctx, calling
// deliver on every event. A transport failure is retried with capped
// exponential backoff; the reconcile ticker covers the gap meanwhile.
func (s *RedisKeyRingStore) subscribeLoop(ctx context.Context, deliver func()) {
	const (
		minBackoff = 100 * time.Millisecond
		maxBackoff = 30 * time.Second
	)
	backoff := minBackoff
	for ctx.Err() == nil {
		err := s.subscribeOnce(ctx, deliver, func() { backoff = minBackoff })
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.reportError(fmt.Errorf("keyring watch subscription: %w", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// subscribeOnce holds one subscription until it fails or ctx ends.
// onConnected resets the caller's backoff once the subscription is live,
// so a long-lived connection that eventually drops retries promptly.
func (s *RedisKeyRingStore) subscribeOnce(ctx context.Context, deliver func(), onConnected func()) error {
	pubsub := s.client.Subscribe(ctx, s.eventChan())
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		return err
	}
	onConnected()
	// A rotation may have landed between the last read and this
	// subscription going live; that event went nowhere, so reconcile once
	// on connect rather than waiting for the ticker.
	deliver()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return errors.New("subscription channel closed")
			}
			deliver()
		}
	}
}

// Ensure returns a ring, bootstrapping if empty. Mirrors the file store
// bootstrap pattern; the resulting ring is persisted on bootstrap.
func (s *RedisKeyRingStore) Ensure(ctx context.Context) (*KeyRing, bool, error) {
	r, err := s.Load(ctx)
	if err == nil {
		return r, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	r = NewKeyRing()
	if _, err := r.Generate(); err != nil {
		return nil, false, err
	}
	if err := s.Save(ctx, r); err != nil {
		if !errors.Is(err, ErrKeyRingConflict) {
			return nil, false, err
		}
		// Another node bootstrapped first. Adopt its ring instead of
		// overwriting it — both nodes must sign with the same keys.
		r, err = s.Load(ctx)
		if err != nil {
			return nil, false, err
		}
		return r, false, nil
	}
	return r, true, nil
}
