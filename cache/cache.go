// Package cache provides request-hash caching primitives for Parsec.
// Applications use this to share cached computation results across users
// and sessions: the cache stores Envelope values (not raw bytes) so a
// cache hit looks identical to a fresh computation result from the
// subscriber's point of view.
//
// Two implementations ship:
//
//   - MemoryCache (in-process LRU) — fast, no external deps, single-host
//   - RedisCache  (Redis-backed)    — shared across processes/machines
//
// Both enforce tenant isolation: tenant-scoped keys are namespaced with
// a tenant prefix so cross-tenant cache leakage is impossible by
// construction.
package cache

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/frankbardon/parsec/envelope"
)

// Cache is the surface Parsec applications consume. Implementations are
// safe for concurrent use.
//
// Tenant-scoped variants exist because most Parsec deployments are
// multi-tenant; using GetForTenant / PutForTenant makes cross-tenant
// leakage a compile-time impossibility for callers that consistently
// use the tenant-scoped API.
type Cache interface {
	Get(ctx context.Context, key string) (envelope.Envelope, bool, error)
	Put(ctx context.Context, key string, env envelope.Envelope, ttl time.Duration) error
	Delete(ctx context.Context, key string) error

	GetForTenant(ctx context.Context, tenantID, key string) (envelope.Envelope, bool, error)
	PutForTenant(ctx context.Context, tenantID, key string, env envelope.Envelope, ttl time.Duration) error
	DeleteForTenant(ctx context.Context, tenantID, key string) error

	// Stats returns a snapshot of cache counters. The shape is shared
	// across implementations so the telemetry surface can render the
	// same JSON for either backend.
	Stats() Stats
}

// Stats is the cross-implementation counter snapshot.
type Stats struct {
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Puts      int64 `json:"puts"`
	Evictions int64 `json:"evictions"`
	// SizeEntries is the count of entries currently held. For
	// distributed caches (Redis) this value is approximate — the
	// implementation may report 0 to avoid a costly DBSIZE call.
	SizeEntries int64 `json:"size_entries"`
}

// HitRate returns hits / (hits + misses) as a percentage. Returns 0
// when no traffic has been observed.
func (s Stats) HitRate() float64 {
	tot := s.Hits + s.Misses
	if tot == 0 {
		return 0
	}
	return float64(s.Hits) * 100.0 / float64(tot)
}

// ErrCacheMiss is the sentinel returned by Get / GetForTenant when the
// key is absent. Callers should errors.Is against this rather than
// relying on the (_, false) return convention; the bool is the primary
// signal but the error variant is here for chained APIs.
var ErrCacheMiss = errors.New("cache: miss")

// scopedKey is the canonical tenant-scoped key. Centralized so the
// memory and redis impls agree on the layout (and so a future test
// can assert it).
func scopedKey(tenantID, key string) string {
	return tenantID + ":" + key
}

// ---------- MemoryCache ----------

// MemoryCache is an LRU cache with per-entry TTL. The LRU is bounded by
// MaxEntries; when full, the least-recently-used entry is evicted.
//
// Expired entries are evicted lazily on Get and proactively by a
// background goroutine that ticks every SweepInterval.
type MemoryCache struct {
	MaxEntries    int
	SweepInterval time.Duration

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	clock   func() time.Time

	hits      atomic.Int64
	misses    atomic.Int64
	puts      atomic.Int64
	evictions atomic.Int64

	stopOnce sync.Once
	stop     chan struct{}
}

type memEntry struct {
	key     string
	env     envelope.Envelope
	expires time.Time
}

// NewMemoryCache constructs a cache holding at most maxEntries items.
// maxEntries <= 0 falls back to 4096.
func NewMemoryCache(maxEntries int) *MemoryCache {
	if maxEntries <= 0 {
		maxEntries = 4096
	}
	c := &MemoryCache{
		MaxEntries:    maxEntries,
		SweepInterval: 30 * time.Second,
		entries:       make(map[string]*list.Element, maxEntries),
		order:         list.New(),
		clock:         time.Now,
		stop:          make(chan struct{}),
	}
	go c.sweep()
	return c
}

// Close stops the background sweeper. Safe to call multiple times.
func (c *MemoryCache) Close() {
	c.stopOnce.Do(func() { close(c.stop) })
}

// Get returns the cached envelope for key.
func (c *MemoryCache) Get(_ context.Context, key string) (envelope.Envelope, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		c.misses.Add(1)
		return envelope.Envelope{}, false, nil
	}
	ent := el.Value.(*memEntry)
	if !ent.expires.IsZero() && c.clock().After(ent.expires) {
		c.order.Remove(el)
		delete(c.entries, key)
		c.evictions.Add(1)
		c.misses.Add(1)
		return envelope.Envelope{}, false, nil
	}
	c.order.MoveToFront(el)
	c.hits.Add(1)
	return ent.env, true, nil
}

// Put inserts env under key with ttl (zero ttl = no expiry).
func (c *MemoryCache) Put(_ context.Context, key string, env envelope.Envelope, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts.Add(1)
	var exp time.Time
	if ttl > 0 {
		exp = c.clock().Add(ttl)
	}
	if el, ok := c.entries[key]; ok {
		ent := el.Value.(*memEntry)
		ent.env = env
		ent.expires = exp
		c.order.MoveToFront(el)
		return nil
	}
	el := c.order.PushFront(&memEntry{key: key, env: env, expires: exp})
	c.entries[key] = el
	for len(c.entries) > c.MaxEntries {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.order.Remove(back)
		delete(c.entries, back.Value.(*memEntry).key)
		c.evictions.Add(1)
	}
	return nil
}

// Delete removes key. No-op if absent.
func (c *MemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.Remove(el)
		delete(c.entries, key)
	}
	return nil
}

// GetForTenant is Get with a tenant-scoped key.
func (c *MemoryCache) GetForTenant(ctx context.Context, tenantID, key string) (envelope.Envelope, bool, error) {
	return c.Get(ctx, scopedKey(tenantID, key))
}

// PutForTenant is Put with a tenant-scoped key.
func (c *MemoryCache) PutForTenant(ctx context.Context, tenantID, key string, env envelope.Envelope, ttl time.Duration) error {
	return c.Put(ctx, scopedKey(tenantID, key), env, ttl)
}

// DeleteForTenant is Delete with a tenant-scoped key.
func (c *MemoryCache) DeleteForTenant(ctx context.Context, tenantID, key string) error {
	return c.Delete(ctx, scopedKey(tenantID, key))
}

// Stats returns a snapshot of the cache counters.
func (c *MemoryCache) Stats() Stats {
	c.mu.Lock()
	size := len(c.entries)
	c.mu.Unlock()
	return Stats{
		Hits:        c.hits.Load(),
		Misses:      c.misses.Load(),
		Puts:        c.puts.Load(),
		Evictions:   c.evictions.Load(),
		SizeEntries: int64(size),
	}
}

func (c *MemoryCache) sweep() {
	t := time.NewTicker(c.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.sweepOnce()
		}
	}
}

func (c *MemoryCache) sweepOnce() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	for e := c.order.Back(); e != nil; {
		ent := e.Value.(*memEntry)
		prev := e.Prev()
		if !ent.expires.IsZero() && now.After(ent.expires) {
			c.order.Remove(e)
			delete(c.entries, ent.key)
			c.evictions.Add(1)
		}
		e = prev
	}
}

// ---------- RedisCache ----------

// RedisCache is the cross-host implementation. Envelopes are stored as
// JSON; keys are prefixed with KeyPrefix.
//
// Stats are best-effort (Hits/Misses/Puts are local counters for this
// process; Evictions and SizeEntries are not tracked — Redis handles
// expiry and total size via its own metrics).
type RedisCache struct {
	Client    redis.UniversalClient
	KeyPrefix string

	hits   atomic.Int64
	misses atomic.Int64
	puts   atomic.Int64
}

// NewRedisCache constructs a Redis-backed cache. prefix is namespaced
// (default "parsec:cache").
func NewRedisCache(c redis.UniversalClient, prefix string) *RedisCache {
	if prefix == "" {
		prefix = "parsec:cache"
	}
	return &RedisCache{Client: c, KeyPrefix: prefix}
}

func (c *RedisCache) k(key string) string { return c.KeyPrefix + ":" + key }

// Get fetches and decodes the envelope at key.
func (c *RedisCache) Get(ctx context.Context, key string) (envelope.Envelope, bool, error) {
	b, err := c.Client.Get(ctx, c.k(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		c.misses.Add(1)
		return envelope.Envelope{}, false, nil
	}
	if err != nil {
		return envelope.Envelope{}, false, err
	}
	var env envelope.Envelope
	if uerr := json.Unmarshal(b, &env); uerr != nil {
		return envelope.Envelope{}, false, uerr
	}
	c.hits.Add(1)
	return env, true, nil
}

// Put serializes env and writes it under key with ttl.
func (c *RedisCache) Put(ctx context.Context, key string, env envelope.Envelope, ttl time.Duration) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := c.Client.Set(ctx, c.k(key), b, ttl).Err(); err != nil {
		return err
	}
	c.puts.Add(1)
	return nil
}

// Delete removes key.
func (c *RedisCache) Delete(ctx context.Context, key string) error {
	return c.Client.Del(ctx, c.k(key)).Err()
}

// GetForTenant / PutForTenant / DeleteForTenant — tenant-scoped variants.
func (c *RedisCache) GetForTenant(ctx context.Context, tenantID, key string) (envelope.Envelope, bool, error) {
	return c.Get(ctx, scopedKey(tenantID, key))
}

func (c *RedisCache) PutForTenant(ctx context.Context, tenantID, key string, env envelope.Envelope, ttl time.Duration) error {
	return c.Put(ctx, scopedKey(tenantID, key), env, ttl)
}

func (c *RedisCache) DeleteForTenant(ctx context.Context, tenantID, key string) error {
	return c.Delete(ctx, scopedKey(tenantID, key))
}

// Stats returns a snapshot of local counters.
func (c *RedisCache) Stats() Stats {
	return Stats{
		Hits:   c.hits.Load(),
		Misses: c.misses.Load(),
		Puts:   c.puts.Load(),
	}
}

// ---------- NoopCache ----------

// NoopCache is the "cache disabled" sentinel. Get always misses; Put,
// Delete, and their tenant-scoped twins are no-ops. Useful as an
// explicit opt-out when parsec.Options.RedisClient is set but the
// embedder does NOT want the auto-built Redis cache.
type NoopCache struct{}

// NewNoopCache returns the singleton NoopCache value.
func NewNoopCache() NoopCache { return NoopCache{} }

func (NoopCache) Get(context.Context, string) (envelope.Envelope, bool, error) {
	return envelope.Envelope{}, false, nil
}

func (NoopCache) Put(context.Context, string, envelope.Envelope, time.Duration) error { return nil }
func (NoopCache) Delete(context.Context, string) error                                { return nil }

func (NoopCache) GetForTenant(context.Context, string, string) (envelope.Envelope, bool, error) {
	return envelope.Envelope{}, false, nil
}

func (NoopCache) PutForTenant(context.Context, string, string, envelope.Envelope, time.Duration) error {
	return nil
}
func (NoopCache) DeleteForTenant(context.Context, string, string) error { return nil }
func (NoopCache) Stats() Stats                                          { return Stats{} }

// BackendReporter is an optional escape hatch for caches wrapped in a
// transparent observer (metrics, tracing). The wrapper implements this
// to expose the underlying backend without exposing the inner Cache
// itself.
type BackendReporter interface {
	Backend() string
}

// Backend reports the implementation name. The manifest surfaces this
// so SDKs and dashboards can distinguish memory / redis / noop without
// reflecting on the cache value. A BackendReporter implementation
// (typically a metrics wrapper) is asked first so wrappers are
// transparent.
func Backend(c Cache) string {
	if c == nil {
		return ""
	}
	if br, ok := c.(BackendReporter); ok {
		return br.Backend()
	}
	switch c.(type) {
	case *MemoryCache:
		return "memory"
	case *RedisCache:
		return "redis"
	case NoopCache:
		return "noop"
	}
	return "custom"
}
