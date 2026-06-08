# Rate limiting — Parsec

Load when adding a new ingress point or changing rate-limit policy.

Every abusable ingress point — `Publish`, broker subscribe attempts,
and `RefreshToken` — is gated by a per-key sliding-window budget. The
contract:

1. `ratelimit.Limiter` has two impls: `MemoryLimiter` (single-node)
   and `RedisLimiter` (cross-node, EVALSHA'd Lua script, key format
   `<prefix>:rl:<bucket>:<subject>`).
2. `parsec.Options.RateLimits` (publish / subscribe / token-issue) is
   the operator-facing knob. The zero value disables gating. The CLI
   shorthand is `--publish-rate 100/s --publish-burst 200`.
3. `parsec.Options.Limiter` is the explicit-injection escape hatch;
   when nil and `RateLimits` has any non-zero bucket, parsec builds a
   `RedisLimiter` (when `RedisClient` is set) or a `MemoryLimiter`.
4. Bucket keys are stable: `publish:<mgmt-subject>`,
   `subscribe:<userID>`, `token-issue:<remote-ip>`. Per-channel
   publish rules (`Options.PerChannelPublishLimits`) namespace the
   key as `publish-channel:<rule-pattern>:<subject>`; per-channel
   subscribe rules (`Options.PerChannelSubscribeLimits`) namespace
   the key as `subscribe-channel:<rule-pattern>:<userID>`. Isolated
   channels do not share the global publish/subscribe budget. The
   HTTP middleware in `internal/server/http.go` stamps the subject +
   IP into the request context via `parsec.WithSubject` /
   `parsec.WithRemoteIP`.
5. A budget hit returns `PARSEC_RATE_LIMITED` → Twirp
   `resource_exhausted` → HTTP 429.
6. Per-token override: `auth.Claims.RateLimitOverride` (json `rl`) may
   tighten the budget for a specific mgmt token. Mint with
   `Issuer.IssuePairWithRateLimit`. Absence = use operator default.
7. The manifest exposes the configured defaults (private overrides
   never surface). Metrics:
   `parsec_rate_limit_decisions_total{bucket, result}`.

A new gated ingress point MUST consult the limiter and emit
`PARSEC_RATE_LIMITED` on exhaustion. See
[docs/src/ops/rate-limiting.md](../../docs/src/ops/rate-limiting.md)
for the operator runbook.
