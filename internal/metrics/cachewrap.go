package metrics

import (
	"context"
	"time"

	"github.com/frankbardon/parsec/cache"
	"github.com/frankbardon/parsec/envelope"
)

const (
	cacheOpGet    = "get"
	cacheOpPut    = "put"
	cacheOpDelete = "delete"

	cacheResultHit  = "hit"
	cacheResultMiss = "miss"
)

// cacheObserver wraps a cache.Cache so every operation increments
// parsec_cache_operations_total. The wrapper is transparent — it
// preserves the Stats() shape so telemetry.CacheSource keeps working
// — and adds a per-call result label so dashboards can plot hit rate
// without re-computing from process counters.
type cacheObserver struct {
	inner   cache.Cache
	m       *Metrics
	backend string
}

// WrapCache returns a cache.Cache that records every op into m. When
// m or inner is nil, inner is returned unchanged (no allocation).
//
// The backend label is captured at wrap time via cache.Backend so the
// CacheSize gauge updates carry a stable, bounded value.
func WrapCache(inner cache.Cache, m *Metrics) cache.Cache {
	if m == nil || inner == nil {
		return inner
	}
	return &cacheObserver{inner: inner, m: m, backend: cache.Backend(inner)}
}

func (c *cacheObserver) Get(ctx context.Context, key string) (envelope.Envelope, bool, error) {
	env, ok, err := c.inner.Get(ctx, key)
	c.recordGet(ok, err)
	return env, ok, err
}

func (c *cacheObserver) Put(ctx context.Context, key string, env envelope.Envelope, ttl time.Duration) error {
	err := c.inner.Put(ctx, key, env, ttl)
	c.recordWrite(cacheOpPut, err)
	return err
}

func (c *cacheObserver) Delete(ctx context.Context, key string) error {
	err := c.inner.Delete(ctx, key)
	c.recordWrite(cacheOpDelete, err)
	return err
}

func (c *cacheObserver) GetForTenant(ctx context.Context, tenantID, key string) (envelope.Envelope, bool, error) {
	env, ok, err := c.inner.GetForTenant(ctx, tenantID, key)
	c.recordGet(ok, err)
	return env, ok, err
}

func (c *cacheObserver) PutForTenant(ctx context.Context, tenantID, key string, env envelope.Envelope, ttl time.Duration) error {
	err := c.inner.PutForTenant(ctx, tenantID, key, env, ttl)
	c.recordWrite(cacheOpPut, err)
	return err
}

func (c *cacheObserver) DeleteForTenant(ctx context.Context, tenantID, key string) error {
	err := c.inner.DeleteForTenant(ctx, tenantID, key)
	c.recordWrite(cacheOpDelete, err)
	return err
}

// Backend implements cache.BackendReporter so cache.Backend can see
// through the wrapper and report the underlying implementation.
func (c *cacheObserver) Backend() string { return c.backend }

func (c *cacheObserver) Stats() cache.Stats {
	st := c.inner.Stats()
	// Track size as a gauge too so dashboards don't need to scrape the
	// telemetry JSON for a single number.
	c.m.CacheSize.WithLabelValues(c.backend).Set(float64(st.SizeEntries))
	return st
}

func (c *cacheObserver) recordGet(hit bool, err error) {
	if err != nil {
		c.m.CacheOperations.WithLabelValues(cacheOpGet, ResultFailure).Inc()
		return
	}
	result := cacheResultMiss
	if hit {
		result = cacheResultHit
	}
	c.m.CacheOperations.WithLabelValues(cacheOpGet, result).Inc()
}

func (c *cacheObserver) recordWrite(op string, err error) {
	result := ResultSuccess
	if err != nil {
		result = ResultFailure
	}
	c.m.CacheOperations.WithLabelValues(op, result).Inc()
}
