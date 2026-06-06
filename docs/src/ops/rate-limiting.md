# Rate Limiting

Parsec gates three abusable ingress points with configurable budgets:

| Bucket        | What it gates                       | Default key |
|---------------|-------------------------------------|-------------|
| `publish`     | The Publish RPC                     | mgmt-bearer subject |
| `subscribe`   | Centrifuge subscribe attempts       | userID, when authenticated |
| `token-issue` | The RefreshToken RPC                | remote IP |

When a bucket is exhausted the caller receives a `PARSEC_RATE_LIMITED`
coded error, rendered as Twirp's `resource_exhausted` and HTTP 429 with
an advisory `Reset` window. Single-node mode uses an in-process token
bucket; multi-node mode (when `Options.RedisClient` is set) shares the
budget across nodes via a Redis sorted-set sliding window driven by a
single-round-trip Lua script.

## Quick start (CLI)

```bash
parsec serve \
  --publish-rate 100/s --publish-burst 200 \
  --subscribe-rate 20/s \
  --token-issue-rate 5/s
```

The shorthand format is `<integer>/<window>`. The window accepts the
unit aliases `s`, `m`, `h`, plus any duration `time.ParseDuration` understands
(`500ms`, `2m30s`). An empty rate string disables that bucket.

Environment-variable equivalents (for container deployments):

```bash
PARSEC_PUBLISH_RATE=100/s
PARSEC_PUBLISH_BURST=200
PARSEC_SUBSCRIBE_RATE=20/s
PARSEC_TOKEN_ISSUE_RATE=5/s
```

## Library configuration

```go
import "github.com/frankbardon/parsec/ratelimit"

p, _ := parsec.New(parsec.Options{
    RateLimits: ratelimit.RateLimits{
        Publish:    ratelimit.Limit{Rate: 100, Per: time.Second, Burst: 200},
        Subscribe:  ratelimit.Limit{Rate: 20,  Per: time.Second},
        TokenIssue: ratelimit.Limit{Rate: 5,   Per: time.Second},
    },
})
```

Pass `Options.Limiter` to plug in a custom backend (e.g. a token-bucket
shared with another service). When `Limiter` is nil and any bucket has
`Rate > 0`, Parsec constructs:

* `ratelimit.RedisLimiter` when `Options.RedisClient` is set
* `ratelimit.MemoryLimiter` otherwise

## Cross-node behaviour

Two Parsec nodes pointing at the same Redis (and sharing
`Options.RedisKeyPrefix`) consume one global budget. The Lua script is
EVALSHA'd on first use and reloaded automatically on `NOSCRIPT`. Keys
take the form `<prefix>:rl:<bucket>:<subject>` and inherit
`PEXPIRE(window + 10s)` so abandoned buckets free themselves.

## Per-token override

A mgmt token may carry an `rl` claim that overrides the operator default
**for that subject**. Mint such a token with
`Issuer.IssuePairWithRateLimit`:

```go
override := ratelimit.Limit{Rate: 10, Per: time.Second}
pair, _ := p.Issuer().IssuePairWithRateLimit(sub, channel, ttl, override)
```

Server-side, the override is automatically picked up from the bearer's
claims on every gated call. The default is still applied to subjects
without an override.

Overrides only narrow the budget — they cannot widen it past the
operator-configured ceiling for an unauthenticated path (the token-issue
bucket keys off IP, not the subject).

## Per-channel publish ceilings

Operators can tighten (or loosen) the publish bucket for individual
channels or families of channels by mapping a channel-name pattern to
a `Limit`. The library option is `PerChannelPublishLimits`:

```go
parsec.New(parsec.Options{
    RateLimits: ratelimit.RateLimits{
        Publish: ratelimit.Limit{Rate: 100, Per: time.Second, Burst: 200},
    },
    PerChannelPublishLimits: map[string]ratelimit.Limit{
        // Internal metrics fanout — drown the broker without
        // affecting other publishers.
        "public:hot.metrics.**": {Rate: 500, Per: time.Second},
        // Sensitive admin broadcasts — clamp to a handful per minute.
        "private:admin.alerts.**": {Rate: 5, Per: time.Minute},
    },
})
```

The pattern grammar is the channel-name grammar from
`channels.ParsePattern` (`*` matches one segment, trailing `**`
matches any remaining segments). Invalid patterns abort
`parsec.New` with `PARSEC_INVALID_ARGUMENT` so a typo never silently
disables a rule.

Rule selection: the **most specific** matching rule wins (literal
segments score 4, single-star segments 1, trailing `**` 0; ties
broken by pattern string ascending). The selected rule's `Limit`
overrides the default `Publish` budget for the call, and the bucket
key is namespaced `publish-channel:<pattern>:<subject>` so two rules
on disjoint channels never share a budget. Channels that match no
rule fall through to the default `Publish` bucket
(`publish:<subject>`).

Per-token overrides still apply on top: `auth.Claims.RateLimitOverride`
beats both the rule and the default. Use this when a single tenant
needs a tighter or looser ceiling on top of a global rule.

Per-channel subscribe limits are not yet supported — only the publish
path consults this configuration. A follow-up will extend the
subscribe authorizer to consult the same rule set.

## Manifest discovery

The `Manifest` RPC exposes the default budgets:

```json
{
  "rate_limits": {
    "publish":     {"rate": 100, "per": "1s", "burst": 200},
    "subscribe":   {"rate": 20,  "per": "1s", "burst": 20},
    "token_issue": {"rate": 5,   "per": "1s", "burst": 5}
  }
}
```

Per-channel rules are appended under `per_channel_publish` (omitted
when none configured):

```json
{
  "rate_limits": {
    "publish":     {"rate": 100, "per": "1s", "burst": 200},
    "subscribe":   {"rate": 20,  "per": "1s", "burst": 20},
    "token_issue": {"rate": 5,   "per": "1s", "burst": 5},
    "per_channel_publish": [
      {"pattern": "public:hot.metrics.**", "limit": {"rate": 500, "per": "1s", "burst": 500}}
    ]
  }
}
```

Per-token overrides are private to the issued token and never surface
in the manifest.

## Metrics

The `parsec_rate_limit_decisions_total{bucket,result}` counter
increments on every decision. `bucket` is `publish`, `subscribe`, or
`token-issue`; `result` is `allowed` or `denied`. Alert on
`rate(parsec_rate_limit_decisions_total{result="denied"}[1m]) > 0` to
catch abusive callers.

## Tuning

* **Burst > Rate.** Configure `burst` ~2x `rate` for bursty
  human-driven workloads; keep them equal for machine workloads where
  rate violations indicate misconfiguration.
* **Per-IP token-issue.** The refresh endpoint has no mgmt bearer, so
  the IP is the only stable key. If Parsec sits behind a load balancer,
  put a complementary L7 rate limit on the LB and keep the parsec
  budget loose — the LB sees the real client IP, parsec sees only the
  LB.
* **Single-node fallback.** `MemoryLimiter` is fine for dev and small
  deployments. Distinct keys live in a shared map; call `Sweep`
  periodically (or rely on the implicit drop-on-touch) if your key
  cardinality is large.

## Failure modes

* **Redis unreachable.** A failed `Allow` returns the underlying
  redis error, which the RPC layer translates to `PARSEC_INTERNAL`
  (HTTP 500). The publish/subscribe paths fail closed — operators
  should monitor `parsec_rate_limit_decisions_total` divergence from
  request totals to detect this case.
* **Mixed-version clusters.** EVALSHA / NOSCRIPT fallback handles
  rolling upgrades automatically.
