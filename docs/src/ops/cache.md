# Request-Hash Cache

The `cache` package gives Parsec applications a request-hash cache for
sharing computed results across users and sessions. The cache stores
`envelope.Envelope` values (not raw bytes), so a hit looks identical to
a fresh computation from the subscriber's point of view.

Two implementations ship — pick the one that fits the deployment:

| Backend | Use case |
|---|---|
| `cache.NewMemoryCache(maxEntries)` | Single-node dev/test or per-process hot path. LRU + per-entry TTL; bounded by `maxEntries` with a background sweeper. |
| `cache.NewRedisCache(client, prefix)` | Multi-node production. Shared keyspace under `<prefix>` (default `parsec:cache`); per-entry TTL via Redis `EX`. |
| `cache.NewNoopCache()` | Explicit opt-out. Every `Get` misses; writes drop. Useful when `Options.RedisClient` is set but you don't want the auto-built Redis cache. |

## Wiring

```go
import (
    "github.com/frankbardon/parsec"
    "github.com/frankbardon/parsec/cache"
)

p, err := parsec.New(parsec.Options{
    KeyRing: ring,
    // Either pass an explicit cache:
    Cache:       cache.NewMemoryCache(8192),
    // ...or set RedisClient and parsec.New auto-builds a RedisCache:
    // RedisClient: redisClient,
})
```

Access the wired cache via `p.Cache()`. When no cache is configured,
`p.Cache()` returns `nil` — embedders that consume the cache
opportunistically should treat that as "caching disabled."

The manifest exposes both states so SDKs and dashboards can pick the
right behavior without sniffing internals:

```json
{
  "kind": "parsec.manifest",
  "payload": {
    "cache_enabled": true,
    "cache_backend": "redis"
  }
}
```

`cache_backend` is one of `memory`, `redis`, `noop`, `custom`, or
empty (cache disabled).

## Tenant scoping

The cache surface ships paired tenant-scoped methods for every
operation — `GetForTenant`, `PutForTenant`, `DeleteForTenant`. Keys
are namespaced with `<tenantID>:` so cross-tenant leakage is
impossible by construction when call sites consistently use the
tenant-scoped API.

```go
cached, ok, _ := p.Cache().GetForTenant(ctx, tenantID, key)
if !ok {
    env := computeExpensiveResult(ctx, ...)
    _ = p.Cache().PutForTenant(ctx, tenantID, key, env, 5*time.Minute)
    cached = env
}
```

## Metrics

Every cache operation increments the bundled Prometheus collectors:

| Metric | Type | Labels |
|---|---|---|
| `parsec_cache_operations_total` | counter | `op` (`get`/`put`/`delete`), `result` (`hit`/`miss`/`success`/`failure`) |
| `parsec_cache_size_entries` | gauge | `backend` (`memory`/`redis`/`noop`/`custom`) |

The `size_entries` gauge is sampled at every `Stats()` call. Redis
caches report their local view (this process's hits/misses); operators
who want a global view should consult Redis' own `INFO memory`.

## Telemetry adapter

The aggregated `/parsec/metrics` JSON view picks up cache hit rate via
the `telemetry.CacheSource`. The adapter wires it in one line:

```go
import "github.com/frankbardon/parsec/telemetry"

agg := telemetry.New(
    channelSrc,
    envSrc,
    tokenSrc,
    telemetry.NewCacheSourceFromCache(p.Cache()), // nil-safe
)
opts.TelemetryHandler = agg.Handler()
```

When `p.Cache()` is nil, `NewCacheSourceFromCache` returns nil and the
aggregator's other sources keep working.

## Adding a new backend

A custom backend must:

1. Implement the full `cache.Cache` interface (`Get`/`Put`/`Delete`
   plus the three tenant-scoped twins plus `Stats`).
2. Return a `cache.Stats` struct with `Hits`/`Misses`/`Puts`/`Evictions`/
   `SizeEntries` so the telemetry adapter and the metrics wrapper read
   stable numbers.
3. Optionally implement `cache.BackendReporter` if the backend should
   identify itself in the manifest as something other than `custom`.

Pass it to `parsec.Options.Cache` and the rest of the wiring — manifest
exposure, metrics, telemetry — works without further changes.
