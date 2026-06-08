# Request-hash cache — Parsec

Load when changing `cache/` or wiring cache through `parsec.Options`.

Embedders share computation results across users via
`parsec.Options.Cache` (or auto-build from `RedisClient`). Two impls
ship: `MemoryCache` (LRU + TTL + background sweeper) and `RedisCache`
(cross-host, JSON-encoded envelopes under a configurable prefix).
`NoopCache` is the explicit opt-out when `RedisClient` is set but the
embedder doesn't want the auto-built Redis cache.

Access via `p.Cache()`; backend label via `p.CacheBackend()` (`"memory"`
/ `"redis"` / `"noop"` / `"custom"` / `""`). The manifest exposes
`cache_enabled` and `cache_backend`. Every operation flows through a
metrics wrapper (`internal/metrics/cachewrap.go`) emitting
`parsec_cache_operations_total{op,result}` and
`parsec_cache_size_entries{backend}`. The telemetry aggregator picks
the cache up via `telemetry.NewCacheSourceFromCache(p.Cache())` —
nil-safe so a cache-less deployment composes the same way.

See [docs/src/ops/cache.md](../../docs/src/ops/cache.md).
