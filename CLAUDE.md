# CLAUDE.md — Parsec

This file is loaded by Claude Code on every session start. It is the
authoritative contract for how Parsec is built, tested, and extended.

## What Parsec is

Parsec is a realtime messaging engine on top of the
[Centrifuge](https://github.com/centrifugal/centrifuge) OSS library. It
ships as a Go library (primary), a CLI (thin adapter), and a
Twirp-JSON RPC server (full external control).

The library is the deliverable. Every other surface is a translator.

## Repository layout

```
parsec.go              # public library facade
auth/                  # HMAC-SHA256 JWT signer/verifier/issuer + subscribe authorizer
broker/                # centrifuge.Node wrapper
channels/              # name grammar + lifecycle manager + TTL
sinks/                 # Sink iface + email / slack / webhook impls
service/               # surface-agnostic business logic (used by CLI, RPC)
rpc/                   # Twirp JSON wire — generated from rpc/service.proto (pb.go + twirp.go)
descriptor/            # manifest envelope
errors/                # coded errors (PARSEC_*)
internal/cli/          # CLI command bodies
internal/codegen/      # parsec-gen Go + TS emitters (driven from schema registry)
internal/server/       # HTTP mux (twirp + websocket + sse + healthz)
internal/rpcclient/    # CLI adapter onto the generated Twirp JSON client
cmd/parsec/            # main.go assembler
cmd/parsec-gen/        # codegen binary; reads schema registry, emits Go/TS bindings
docs/                  # mdBook source
```

## Non-negotiable conventions

### Library-first
All business logic lives in library packages. `cmd/parsec/main.go` and
`internal/cli/*` only parse flags and call into the library. If a CLI
subcommand grows logic that the library cannot also call, the CLI is wrong.

### Surface parity
Anything reachable over Twirp must also be reachable from the CLI.
The Twirp service definition in `rpc/service.proto` is the contract;
`rpc/service.pb.go` and
`rpc/service.twirp.go` are generated from it by `make proto` and are the
source of truth for the wire format. Edit the proto, then regenerate —
never edit the generated files by hand.

### Channel naming
The grammar is `<visibility>:<app>.<domain>[.<id>][.<topic>]`. There is
exactly one validator: `channels.ParseName`. Every surface calls it.
Do not reimplement it.

| Rule | Where |
|---|---|
| Visibility must be `public` or `private` | `channels/name.go` |
| Private channels MUST have an id segment | `channels/name.go` |
| Private TTL is capped at 1h | `channels/manager.go` |
| Components are lowercase ASCII + digits + `-` + `_` | `channels/name.go` |
| `:` is reserved for the visibility prefix | `channels/name.go` |

### Errors are coded
All errors that cross a package boundary are typed `*errors.Error` with a
`PARSEC_DOMAIN_CATEGORY` code. RPC mapping happens once in
`service/twirp_errors.go:toTwirpError` (server side, PARSEC_* → twirp
codes) and `internal/rpcclient/errors.go:mapErr` (client side, twirp
codes → PARSEC_*). CLI surfaces the code via the descriptor envelope.

### Filesystem access
Library code uses `afero.Fs`. Never `os.Open` / `os.ReadFile` inside library
packages. The CLI is the one allowed exception (e.g. reading a `--file`
argument).

### Descriptor envelopes
Every JSON output (CLI `--json`, `/manifest`) wraps
its payload in `descriptor.Envelope`. The format version is bumped on any
wire-shape change.

### No business logic in `cmd/`
`cmd/parsec/main.go` assembles the command tree. Nothing else.

### Manager ↔ broker contract
The channel manager owns lifecycle. It emits `Event{Kind, Name, At}` on
every transition; the broker translates events into wire actions.

| Event | Wire effect |
|---|---|
| `opened` | New subscribers accepted (subject to auth) |
| `closed` (public, TTL exceeded) | No new arrivals; existing subscribers drain — NOT kicked |
| `deleted` | Every connected subscriber is unsubscribed |

`Manager.Subscribe(chan<- Event)` registers a consumer; the manager does
non-blocking sends, so use a buffered channel. `parsec.New` composes the
subscribe authorizer so closed/deleted channels reject new subscribes
even before the bridge runs.

### Request-hash cache (`cache/`)
Embedders share computation results across users via `parsec.Options.Cache`
(or auto-build from `RedisClient`). Two impls ship: `MemoryCache` (LRU
+ TTL + background sweeper) and `RedisCache` (cross-host, JSON-encoded
envelopes under a configurable prefix). `NoopCache` is the explicit
opt-out when `RedisClient` is set but the embedder doesn't want the
auto-built Redis cache.

Access via `p.Cache()`; backend label via `p.CacheBackend()` ("memory"
/ "redis" / "noop" / "custom" / ""). The manifest exposes
`cache_enabled` and `cache_backend`. Every operation flows through a
metrics wrapper (`internal/metrics/cachewrap.go`) emitting
`parsec_cache_operations_total{op,result}` and
`parsec_cache_size_entries{backend}`. The telemetry aggregator picks
the cache up via `telemetry.NewCacheSourceFromCache(p.Cache())` —
nil-safe so a cache-less deployment composes the same way. See
[docs/src/ops/cache.md](../docs/src/ops/cache.md).

### Token broker (`tokenbroker/`)
The token broker is the policy point for user-facing connection tokens.
The library mints tokens via `auth.Issuer`; the broker sits in front and
adds:

- Channel ACL via a pluggable `Authorizer` (RoleAuthorizer / AllowAll /
  custom).
- Delegated issuance (`/parsec/token/delegate`) where a backend service
  mints "on behalf of" a user; both identities land in the audit log.
- Revocation (`/parsec/revoke`) backed by `RevocationStore`. Two impls
  ship: `MemoryRevocations` (single-node; entries age out past
  `MaxTTL`; call `StartPruner` to reclaim memory) and `RedisRevocations`
  (multi-node; SET with EX `MaxTTL`).

Every access token now carries a unique `jti` claim so single-token
revocation works. Subscribe-side revocation is plumbed via
`parsec.Options.RevocationStore` — wired identically on the broker
side, the subscribe authorizer consults the store on every private
channel attempt and denies revoked tokens with PARSEC_AUTH_DENIED. A
deployment that exposes `/parsec/revoke` but leaves
`Options.RevocationStore` nil cannot deny mid-flight tokens; the
manifest's `revocation_store_enabled` flag surfaces that gap. See
[docs/src/ops/token-broker.md](../docs/src/ops/token-broker.md).

Operators reach the store via the Twirp service surface:
`RevokeToken(token_id, user_id?, reason?)` and
`RevokeUser(user_id, reason?)`. Both are gated by the mgmt bearer
(distinct from the user-facing `/parsec/revoke` HTTP route, which
authenticates with the user's own bearer via the broker's
`Authenticator`). The CLI ships `parsec tokens revoke` and
`parsec tokens revoke-user`. Both RPCs return PARSEC_INVALID_ARGUMENT
when no `RevocationStore` is wired so misconfiguration fails loudly
instead of no-op'ing.

### Auth is JWT with `kid`-based key rotation; HS256 default, RS256/EdDSA/ES256/ES384 optional
The `auth/` package mints three token types:

| Type | TTL default | Purpose |
|---|---|---|
| `access` | 5m (clamped [1m, 1h]) | Connect over websocket + subscribe to listed private channels |
| `refresh` | min(channel TTL, 1h) | Exchange at `RefreshToken` RPC for a fresh access + fresh refresh (rotated per redemption; reuse triggers family revoke — see `docs/src/ops/refresh-rotation.md`) |
| `mgmt` | 24h (clamped [1h, 7d]) | `Authorization: Bearer` on the management RPC |

Tokens are compact JWTs. The JOSE header is **fixed per key** —
`{"alg":"<HS256|RS256|EdDSA|ES256|ES384>","kid":"<kid>","typ":"JWT"}`.
The verifier refuses any unknown `alg`/`typ`, refuses tokens without
a `kid`, and refuses tokens whose declared `alg` does not match the
algorithm of the key the `kid` points to (defends against key
confusion). For ECDSA the verifier also rejects signatures whose
length isn't exactly `2 * curve coord size` so a DER-shaped
signature cannot be smuggled in. The `kid` is looked up in a
`KeyRing` to fetch the verifying key material.

The KeyRing holds N≥1 keys, exactly one with role `active` (the
signer). Each key carries an `Alg` (HS256 / RS256 / EdDSA / ES256 /
ES384); a single ring can mix algorithms. Others are `verify-only`.
Retired keys stop verifying immediately and drop from the next
snapshot.

Asymmetric public keys are exposed via JWKS at `/parsec/jwks.json`
when at least one non-retired asymmetric key is in the ring. HMAC
keys are NEVER exposed there — they are shared secrets, not
verifying material. See
[docs/src/ops/asymmetric-signing.md](../docs/src/ops/asymmetric-signing.md).

Persistence: `parsec.Options.StateDir` makes the ring file-backed at
`<StateDir>/keyring.json` (mode `0600`, parent `0700`). Without
StateDir the ring is ephemeral and tokens do not survive a restart.

Rotation: `parsec keys generate` → `parsec keys promote <kid>` →
`parsec tokens mgmt` (mint a fresh bearer under the new key) → wait the
longest token TTL → `parsec keys retire <old-kid>`. The runbook in
[docs/src/ops/key-rotation.md](../docs/src/ops/key-rotation.md) has the
full procedure including break-glass.

Reload: SIGHUP, `parsec keys reload`, or the mtime-poll watcher (5s
default, configurable via `--keyring-poll`).

Refresh-token rotation: every refresh carries a `jti` (per-token ID)
and an `fid` (rotation-family ID). `RefreshToken` mints a fresh
access + fresh refresh in the same family; the old `jti` is marked
redeemed in a `RefreshStore` (memory single-node, Redis multi-node).
A second redemption of the same `jti` is reuse — the entire family
is revoked. Legacy refresh tokens without `jti` short-circuit to the
old "mint access only" path. See
[docs/src/ops/refresh-rotation.md](../docs/src/ops/refresh-rotation.md).

`Manifest`, `RefreshToken` skip the bearer middleware. Every other RPC
requires a valid `mgmt` token signed by a non-retired key.

#### OIDC bridge (optional)

When `parsec.Options.OIDCConfig` is non-nil (or the YAML config has
`auth.oidc.issuer` set), parsec also accepts ID tokens issued by the
configured OpenID provider as mgmt bearers. The HMAC verifier runs
first; on failure the OIDC verifier (`auth.OIDCVerifier`, wrapping
`github.com/coreos/go-oidc/v3`) re-checks against the issuer's JWKS.
The composite is `auth.CompositeVerifier`. Successful OIDC tokens
are translated into a synthetic `auth.Claims` with `Typ=TypeMgmt` so
the rate limiter, scope authorizer, and access log treat both kinds
uniformly. `auth.OIDCConfig.Grants` maps IdP group names onto parsec
scope patterns + verb sets, so role-based access lives in the IdP
without parsec needing per-user state. CLI: `parsec login oidc` runs
the device-code flow and writes the ID token to `~/.parsec/credentials`
(0600); `parsec logout` deletes the file. See
[docs/src/ops/oidc.md](../docs/src/ops/oidc.md) for the full
walkthrough including Google / Okta / Keycloak examples. Deployments
without `OIDCConfig` accept HMAC mgmt tokens only.

#### Token claims and scopes

Access and refresh tokens carry an exact-match channel list (`chs`) and
an optional set of pattern-based grants (`scopes`). Each scope is
`{pattern, verbs, deny?}` where pattern uses the channel grammar plus
`*` (single segment) and trailing `**` (multi-segment). Recognized
verbs are `subscribe`, `publish`, `manage`. The authorization check at
every surface is the union of `chs` (exact match — any verb) and
`scopes` (pattern match + verb). Legacy tokens carrying only `chs`
continue to work without modification — the new feature is purely
additive.

Scopes can also be **negative**: a scope with `deny: true` subtracts
its matched (channel, verb) pair from the grant set with **deny-wins**
precedence (matching AWS IAM and similar). The evaluation order is
deny pass → `chs` pass → allow pass; any matching deny rejects the
request even if `chs` or an allow scope would otherwise have permitted
it. Deny patterns are visible in the token (signed but not encrypted),
so do not encode secret channel names in a deny pattern. The manifest
exposes `deny_supported: true` and `scope_precedence: "deny-wins"`.
See `docs/src/channels/acl.md` for the grammar, worked example, and
the security note.

## Surfaces in detail

### CLI (`internal/cli/`)
- `parsec serve`              — boots broker + HTTP surface
- `parsec channels list/open/create/get/delete`
- `parsec publish <name>`     — read body from `--data`, `--file`, or stdin
- `parsec subscribe <name>`   — SSE probe (production clients use websocket)
- `parsec --json`             — emit manifest envelope

Defaults: `--server` is `http://localhost:8000` (env: `PARSEC_SERVER`).
Auth: `--token` (env: `PARSEC_TOKEN`).

### Twirp (`rpc/`)
Wire format: Twirp v8 JSON. Path prefix: `/twirp/parsec.ParsecService/`.
Field names on the wire are lowerCamelCase (the protojson default). All
methods:
- `Manifest` → descriptor envelope as bytes (public, no bearer)
- `OpenPublic`, `CreatePrivate`, `ListChannels`, `GetChannel`, `DeleteChannel`
- `Publish`, `Presence`
- `RefreshToken` (public — refresh token authenticates), `IssueMgmt`
- `ListKeys`, `GenerateKey`, `PromoteKey`, `RetireKey`, `ReloadKeys`

`rpc/service.pb.go` and `rpc/service.twirp.go` are generated by
`make proto` from `rpc/service.proto` and ARE the source of truth.
The generated server is mounted by `internal/server/http.go`, which
also installs the bearer middleware that gates every method except
`Manifest` and `RefreshToken`. The CLI talks to the server through
`internal/rpcclient`, which wraps the generated JSON client and injects
the `Authorization: Bearer` header via an `http.RoundTripper`.

### WebSocket (`internal/server/`)
Mounted at `/connection/websocket`. Centrifuge's default WebsocketHandler.
Browser clients connect with `centrifuge-js`.

### HTTP-streaming (`internal/server/http.go`)
Mounted at `/connection/http_stream`. Centrifuge's default
`HTTPStreamHandler` — bidirectional emulation transport. The client
POSTs the connect command and the server streams newline-delimited
JSON (or octet-protobuf) frames back. Production-grade fallback for
clients on networks that block WebSocket upgrades; advertised in the
manifest's `transports` list as `http_stream`. See
[docs/src/ops/http-stream.md](../docs/src/ops/http-stream.md).

### SSE (`internal/server/sse.go`)
Tiny polling-backed Server-Sent Events stream at `/sse?channel=<name>`.
Used only by the CLI `subscribe` probe — not a production transport.

### JavaScript client (`clients/js/`)
`clients/js/` holds the `@frankbardon/parsec-client` npm package — a
composition-only wrapper around `centrifuge-js` v5.x. The wrapper adds
the Parsec conventions a browser client needs: token refresh + rotation
via the Twirp `RefreshToken` RPC, channel-name validation (port of
`channels/name.go`), manifest-driven transport selection, coded
errors that mirror `errors/codes.go`, and an unverified-only scope
inspector for UIs.

Rules:
- Composition over `centrifuge-js`, never subclassing. Anything
  centrifuge-js already does is not re-implemented.
- Anything Parsec-specific lives here. Anything the Go server cannot
  also model belongs nowhere — the wrapper transports policy, never
  invents it.
- The channel grammar parser in `clients/js/src/channels.ts` is a
  port of `channels/name.go` and MUST stay in sync — see the Update
  Demand row.
- Coded errors in `clients/js/src/errors.ts` mirror `errors/codes.go`
  one-for-one. Twirp JSON → coded error mapping mirrors
  `internal/rpcclient/errors.go`.
- The descriptor envelope parser in `clients/js/src/manifest.ts`
  reads `format_version`. A bump in `descriptor.FormatVersion` is a
  wire break and must update the JS client in the same PR.
- The package versions independently from the Go server. Each npm
  release declares a minimum server version in its CHANGELOG.

### Observability
Every Parsec deployment is observable. Three streams:

- **Prometheus metrics** at `/metrics` (bearer-gated when
  `Options.MetricsBearerToken` is set). Collectors live in
  `internal/metrics/registry.go`. Cardinality budget: visibility
  (`public`/`private`), result enums, and bounded sink/method names
  ONLY. Channel names, subject IDs, and bearer tokens are NEVER
  labels — they are an unbounded cardinality footgun.
- **OpenTelemetry tracing** via `internal/tracing/tracer.go`. Off by
  default; set `Options.OTLPEndpoint` to enable. The no-op tracer
  performs zero allocations.
- **Structured access logs** via `internal/server/access_log.go`.
  One slog INFO line per HTTP request with `method`, `path`,
  `status`, `duration_ms`, `request_id`, `remote_addr` (XFF-aware
  via `Options.TrustedProxies`), `bearer_subject` (best-effort
  base64 decode, never verified), `trace_id`. Token contents are
  NEVER logged. Auth failures log at WARN with a `PARSEC_AUTH_*`
  code only.

Adding a new subsystem with internal state? Emit a metric or document
why none is needed. See `docs/src/ops/observability.md` for the metric
reference and Grafana starter dashboard.

#### Telemetry aggregator (`telemetry/`)

`telemetry.Aggregator` composes per-source dashboard stats into a
single `Snapshot` served as JSON at `/parsec/metrics` (via
`Options.TelemetryHandler`). The aggregator also supports:

- **Declarative alert rules** via `Aggregator.WithAlerts([]AlertRule)`.
  Each rule has a `Name`, `Severity` (`info`/`warning`/`critical`),
  `Description`, and `Condition func(Snapshot) bool`. Rules fire on
  every Snapshot and surface in `Snapshot.Alerts`. Validation runs at
  `WithAlerts` time — duplicate names, empty names, nil conditions, or
  unknown severities abort boot.
- **Prometheus text exposition** via `Aggregator.PrometheusHandler()`.
  Re-aggregates on every scrape and renders each Snapshot field as a
  `parsec_telemetry_*` gauge; firing alerts surface as
  `parsec_telemetry_alerts_firing{alert,severity}`. Cardinality budget
  same as `/metrics`: only bounded labels (`pattern`, `aspect`,
  `alert`, `severity`) escape.

The aggregator is opt-in — embedders construct it and wire
`Options.TelemetryHandler`. See
[docs/src/ops/telemetry-alerts.md](../docs/src/ops/telemetry-alerts.md).

## Sink reliability

`PublishOrSink` retries transient sink failures and lands terminal
failures in a dead-letter queue. The contract:

1. Every sink in `parsec.Options.Sinks` is wrapped in a `sinks.Retrier`
   at construction time. The wrap is idempotent — wrapping an already-
   wrapped sink is a no-op.
2. The default retry policy is 5 attempts with exponential backoff
   (1s → 30s) and 20% jitter. Override with `Options.SinkRetry` or
   `Options.PerSinkRetry[<name>]`.
3. Sinks classify errors by wrapping with `sinks.Transient(err)` or
   `sinks.Terminal(err)`. The default `sinks.IsTransient` heuristic
   covers std-library network errors, HTTP 5xx, 429, and SMTP 4xx.
4. When retries are exhausted (or a terminal error is observed
   immediately) the `Retrier` pushes a `DLQItem` and returns `nil` to
   `PublishOrSink`. The caller only sees an error when the DLQ push
   itself fails (`PARSEC_SINK_DLQ_OVERFLOW`).
5. The DLQ backend is `sinks.MemoryDLQ` in single-node mode and
   `sinks.RedisDLQ` (one stream per sink, `parsec:dlq:<sink>`) in
   multi-node mode. `Options.DLQ` overrides both.
6. `parsec dlq {list,count,discard,replay}` is the CLI for inspecting
   and operating on the DLQ.

See [docs/src/ops/dlq.md](../docs/src/ops/dlq.md) for the operator-side
runbook.

## Rate limiting

Every abusable ingress point — `Publish`, broker subscribe attempts,
and `RefreshToken` — is gated by a per-key sliding-window budget. The
contract:

1. `ratelimit.Limiter` has two impls: `MemoryLimiter` (single-node) and
   `RedisLimiter` (cross-node, EVALSHA'd Lua script, key format
   `<prefix>:rl:<bucket>:<subject>`).
2. `parsec.Options.RateLimits` (publish / subscribe / token-issue) is
   the operator-facing knob. The zero value disables gating. The CLI
   shorthand is `--publish-rate 100/s --publish-burst 200`.
3. `parsec.Options.Limiter` is the explicit-injection escape hatch;
   when nil and `RateLimits` has any non-zero bucket, parsec builds a
   `RedisLimiter` (when `RedisClient` is set) or a `MemoryLimiter`.
4. Bucket keys are stable: `publish:<mgmt-subject>`,
   `subscribe:<userID>`, `token-issue:<remote-ip>`. Per-channel publish
   rules (`Options.PerChannelPublishLimits`) namespace the key as
   `publish-channel:<rule-pattern>:<subject>`; per-channel subscribe
   rules (`Options.PerChannelSubscribeLimits`) namespace the key as
   `subscribe-channel:<rule-pattern>:<userID>`. Isolated channels do
   not share the global publish/subscribe budget. The HTTP middleware
   in `internal/server/http.go` stamps the subject + IP into the
   request context via `parsec.WithSubject` / `parsec.WithRemoteIP`.
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
[docs/src/ops/rate-limiting.md](../docs/src/ops/rate-limiting.md) for
the operator runbook.

## Build, test, lint

```bash
make build            # bin/parsec
make test             # go test ./...
make lint             # go vet + staticcheck
make cover            # coverage profile
make proto            # regenerate rpc/ from service.proto (requires protoc + protoc-gen-twirp)
make docs             # mdbook build
```

CGO is disabled in the Makefile. Anything that pulls a C toolchain fails
the build.

## The Update Demand

Any change to a public surface MUST update its docs in the same PR.

| Trigger | Update |
|---|---|
| New CLI subcommand | `docs/src/cli/` page + `docs/src/SUMMARY.md` |
| New RPC method | `rpc/service.proto` + run `make proto` (commit generated `service.pb.go` + `service.twirp.go`) + adapter method in `service/adapter.go` (or `service/keys_adapter.go`) + `internal/rpcclient/` + `internal/cli/` |
| New channel namespace | `docs/src/channels/naming.md` + a test in `channels/name_test.go` |
| New error code | `errors/codes.go` + server-side mapping in `service/twirp_errors.go` + client-side mapping in `internal/rpcclient/errors.go` + `clients/js/src/errors.ts` `ParsecErrorCode` enum + Twirp mapper + a row in `clients/js/test/errors.test.ts` |
| New sink | `sinks/<name>/` package + manifest exposure via registry + MUST declare transient/terminal classification by wrapping errors with `sinks.Transient(err)` / `sinks.Terminal(err)`; sinks with a per-call recipient MUST implement `sinks.RecipientDecoder` so Redis-DLQ Replay can rebuild the typed Recipient |
| New auth token type | `auth/claims.go` (Type const + Valid()) + verifier round-trip test + manifest exposure if applicable |
| New key role | `auth/keyring.go` Role const + tests + `keyring.json` format_version bump if persisted differently |
| New key-management RPC | `rpc/service.proto` + run `make proto` + `service/keys.go` + `service/keys_adapter.go` + `internal/rpcclient/keys.go` + `internal/cli/keys.go` |
| New scope verb | `auth/scope.go` (Verb const + `Valid()` + `AllVerbs`) + verb-gating branch in `Scope.Authorizes` + truth-table test in `auth/scope_test.go` + manifest exposure via `descriptor.Manifest.SupportedVerbs` + `docs/src/channels/acl.md` |
| New subsystem with state | A Prometheus collector in `internal/metrics/registry.go` (or a documented justification for none) + entry in `docs/src/ops/observability.md` metric reference |
| New ingress point | `service.Service` call site consulting `Parsec.CheckRateLimit` before doing work; documented bucket key; entry in `docs/src/ops/rate-limiting.md` |
| New telemetry Snapshot field | `telemetry/telemetry.go` Source method + Aggregator sum branch + corresponding `parsec_telemetry_*` gauge in `telemetry/prom.go` + metric-reference row in `docs/src/ops/telemetry-alerts.md` |
| New severity level | `telemetry/alerts.go` Severity const + `Valid()` branch + table row in `docs/src/ops/telemetry-alerts.md` |
| New `tokenbroker.RevocationStore` impl | implement all four interface methods + apply `MaxTTL` semantics + tests for token-scope + user-scope round trips + entry in `docs/src/ops/token-broker.md` |
| New `cache.Cache` backend | implement all 7 interface methods + return a stable `cache.Stats` shape + optional `cache.BackendReporter` for manifest label + entry in the backend table in `docs/src/ops/cache.md` |
| Change to `channels/name.go` grammar | port to `clients/js/src/channels.ts` in the same PR + mirror the new test case in `clients/js/test/channels.test.ts` |
| Bump `descriptor.FormatVersion` | update `clients/js/src/manifest.ts` envelope parser + `clients/js/src/types.ts` Envelope/Manifest shape + a fixture refresh in `clients/js/test/manifest.test.ts` |
| New transport mounted in `internal/server/` | add to the `TransportName` union in `clients/js/src/types.ts` + extend `clients/js/src/manifest.ts` `transportPath` + doc page row in `docs/src/getting-started/js-client.md` |
| Refresh-token RPC wire change | update `RefreshTokenRequest`/`RefreshTokenResponse` in `clients/js/src/types.ts` + `clients/js/src/refresh.ts` mapping + a refresh fixture in `clients/js/test/refresh.test.ts` |

## Anti-patterns to refuse

- A new `internal/` package that duplicates `service/` logic.
- A CLI command that calls `broker.*` or `channels.*` directly. Go through
  `service.Service`.
- Error strings instead of coded errors at a package boundary.
- A new channel-validation helper outside `channels/`.
- Adding a feature to Parsec that the centrifuge OSS lib cannot deliver. The
  reflex should be: "is this a primitive parsec should ship, or a sink the
  consumer should compose?"
- A JS-client feature the Go server cannot also model. The wrapper transports
  policy, never invents it. If `clients/js/` grows authorization or schema
  decisions, the decision belongs in `service/` and the client just plumbs
  the verdict.
- A second JavaScript package in this repo. One client, one wrapper. New
  language clients live in `clients/<lang>/` — not under a separate
  `clients/js-extras/` or `clients/js2/`.
- A `clients/js/src/` module that imports `centrifuge` types into the
  `types.ts` public surface. Scope-inspector-only consumers must be able
  to import `types.ts` without dragging centrifuge-js into the type graph.
