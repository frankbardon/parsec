# Parsec

> Realtime messaging engine for Go. Public + private channels, token-gated
> ACLs, out-of-band sink fallback, optional Redis cluster, optional
> WebTransport, and a self-describing wire — all behind one library import.

Parsec wraps the [Centrifuge](https://github.com/centrifugal/centrifuge) OSS
library with a small, opinionated set of primitives: a channel naming
grammar, a TTL-driven lifecycle manager, HMAC + OIDC mgmt tokens with
zero-downtime key rotation, sink retry + Redis DLQ, sliding-window rate
limits, and a single descriptor envelope every surface honors.

It ships as a Go library (the deliverable), a CLI binary, and a Twirp
JSON RPC service — three surfaces over the same engine.

```bash
# Docker (fastest eval — multi-arch, ~7 MB image)
docker run -p 8000:8000 ghcr.io/frankbardon/parsec:latest

# Go install (build from source)
go install github.com/frankbardon/parsec/cmd/parsec@latest
```

For the full local stack (parsec + redis + reference YAML config),
`docker compose up --build` from the repo root.

---

## When to reach for Parsec

- You want a websocket / WebTransport pub-sub broker you embed in a Go
  service rather than run as a separate platform.
- You need an opinionated channel namespace (`public:` vs `private:`,
  per-resource segments, TTL caps) instead of a free-for-all topic tree.
- You want token-based subscribe authorization with **pattern-grant
  scopes** and **deny-wins** ACLs, signed with HMAC and rotatable without
  dropping live connections.
- You need a "no-one's listening — page somebody" path (email / slack /
  webhook) with retry + dead-letter, not just a fire-and-forget publish.
- You operate multi-node and want one Redis to back the registry,
  presence, keyring, DLQ, and rate limiter without bolting on five
  separate stores.

It is **not** a durable work queue, not Centrifugo Pro, and not an
opinion about your domain — pick verbs that fit your app.

---

## Highlights

| Capability | Mechanism |
|---|---|
| Channel grammar | `<visibility>:<app>.<domain>[.<id>][.<topic>]` validated by `channels.ParseName` |
| Lifecycle | TTL sweep, close (drain) vs. delete (kick), event bus to broker |
| Auth | HMAC-SHA256 JWT, `kid`-aware KeyRing, zero-downtime rotation |
| OIDC bridge | Optional — composite verifier accepts IdP ID tokens as mgmt bearers |
| Pattern scopes | `*` + `**` glob grants per verb (`subscribe` / `publish` / `manage`), with `deny`-wins precedence |
| Sinks | Email / Slack / Webhook out of the box, `Sink` interface for your own |
| Retry + DLQ | Per-sink retry policy + Redis Streams DLQ (`parsec dlq {list,count,discard,replay}`) |
| Rate limits | Sliding-window per subject/IP, Redis or in-memory, per-token override |
| Persistence | In-memory by default; Redis broker + presence + registry when `Options.RedisClient` is set |
| Delta compression | Per-channel fossil-delta opt-in (`channels.SetDelta`) |
| Observability | Prometheus collectors + OTLP traces + structured access logs |
| Transports | WebSocket (default), HTTP/3 WebTransport (opt-in) |
| Surfaces | Go library, CLI, Twirp JSON RPC |
| Browser client | [`centrifuge-js`](https://github.com/centrifugal/centrifuge-js) drop-in (parsec speaks the Centrifugo wire protocol) |
| Testing | `parsec/parsectest` — `httptest`-style helpers, miniredis cluster variant |
| Container | Multi-arch (`amd64`/`arm64`) distroless image at `ghcr.io/frankbardon/parsec` |
| Config | YAML file + env interpolation, with CLI / env / file / default precedence |
| Multi-region | `parsec keys export/import`, `parsec-keys-sync` pub/sub bridge, manifest peer list |
| Admin UI | Embedded vanilla-JS SPA (`/admin`), opt-in via `Options.AdminUI` |

---

## 60-second quickstart (Docker)

```bash
docker run --rm -p 8000:8000 \
  -v parsec-state:/var/lib/parsec \
  ghcr.io/frankbardon/parsec:latest

# In another shell:
docker logs <container> 2>&1 | grep bootstrap   # capture mgmt token
curl http://localhost:8000/manifest             # public — no bearer
```

The browser end-to-end demo at [`examples/browser/`](examples/browser/)
is `go run ./examples/browser` — one Go file, one HTML file, full
broker → websocket → browser path on <http://localhost:8000>.

## 60-second quickstart (CLI)

Boot the broker. First run mints an HMAC secret and prints a bootstrap
mgmt token to stderr — capture it:

```bash
parsec serve --addr :8000 --state-dir /var/lib/parsec
# parsec serve: bootstrap mgmt token (expires ...):
# eyJhbGciOiJIUzI1NiIs...
```

Authenticate subsequent CLI calls:

```bash
export PARSEC_TOKEN="eyJhbGciOiJIUzI1NiIs..."
export PARSEC_SERVER="http://localhost:8000"
```

Open a public channel, publish, and tail it:

```bash
parsec channels open public:webapp.system.status
echo '{"msg":"hello"}' | parsec publish public:webapp.system.status
parsec subscribe public:webapp.system.status   # SSE probe, NDJSON to stdout
```

Mint a private channel with a one-shot access + refresh token pair:

```bash
parsec channels create private:webapp.user.42.downloads \
  --subject user-42 --ttl 30m
```

Exchange a refresh for a fresh access token (this RPC is public):

```bash
parsec tokens refresh "<refresh-token>"
```

Browser clients connect with `centrifuge-js` at
`ws://localhost:8000/connection/websocket` and present the access token.

---

## Testing against parsec

`github.com/frankbardon/parsec/parsectest` ships test helpers shaped
like `net/http/httptest`. One call returns a running instance bound to
test lifetime:

```go
import "github.com/frankbardon/parsec/parsectest"

func TestMyPublisher(t *testing.T) {
    p := parsectest.New(t)                       // ephemeral keyring, in-memory everything
    ch, _ := p.OpenPublic("public:webapp.system.status", time.Minute)
    p.Publish(context.Background(), ch.Name.String(), []byte(`{"k":"v"}`))
}

func TestMyClient(t *testing.T) {
    inst := parsectest.NewServer(t)              // adds an *httptest.Server
    bearer := inst.MintMgmt(t, "ops", time.Hour)
    // point a Twirp client at inst.BaseURL with bearer ...
}

func TestClusterScenario(t *testing.T) {
    inst := parsectest.NewWithRedis(t)           // miniredis-backed multi-node code paths
}
```

Full reference: [docs/src/library/testing.md](docs/src/library/testing.md).

## Library quickstart (Go)

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/frankbardon/parsec"
)

func main() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    p, err := parsec.New(parsec.Options{
        StateDir: "/var/lib/parsec",   // persistent keyring
    })
    if err != nil {
        log.Fatal(err)
    }
    go func() { _ = p.Run(ctx) }()

    ch, _ := p.OpenPublic("public:webapp.system.status", time.Hour)
    _, _ = p.Publish(ctx, ch.Name.String(), []byte(`{"msg":"hello"}`))

    // PublishOrSink: deliver in-band when subscribers exist; sink otherwise.
    _, _ = p.PublishOrSink(ctx,
        "private:webapp.user.42.downloads",
        []byte(`{"status":"ready"}`),
        "email",
        /* sinks.Recipient */ nil,
        parsec.Message{Subject: "Your download is ready", Body: "..."},
    )
}
```

`parsec.New` assembles the keyring, the
signer/verifier/issuer triad, the channel manager, and the broker.
`Run(ctx)` launches the sweep loop, the manager→broker event bridge, the
keyring watcher, and blocks on the Centrifuge node until the context
cancels.

Runnable scenarios — scoreboards, downloads, agent progress, nightly
reports, heartbeat probes, feedback flagging — live under
[`examples/scenarios/`](examples/scenarios/). Each is one
`go run ./examples/scenarios/<name>` away from booting.

---

## Channel naming

```
<visibility>:<app>.<domain>[.<id>][.<topic>]
```

- `public:webapp.system.status` — broadcast feed, anyone subscribes.
- `private:agent.analysis.{job}.progress` — token-gated, per-job.
- `private:webapp.user.{uid}.downloads` — per-user notifications.

Rules:

| Rule | |
|---|---|
| Visibility | `public` or `private` |
| Private channels | MUST have an id segment |
| TTL cap | Private channels capped at 1h |
| Components | lowercase ASCII + digits + `-` + `_` |
| Reserved | `:` is the visibility separator only |

`channels.ParseName` is the single source of truth and every surface
calls it. See [docs/src/channels/naming.md](docs/src/channels/naming.md).

---

## Production config (YAML)

`parsec serve --config /etc/parsec/parsec.yaml`. Precedence is
**CLI flag > env var > file > built-in default**. Every section is
optional; `${ENV_VAR}` interpolation lets secrets come from the
environment.

```yaml
server:
  addr: ":8000"

auth:
  state_dir: /var/lib/parsec
  mgmt_ttl: 24h
  keyring_poll: 5s

redis:
  addr: redis://localhost:6379
  key_prefix: parsec

manager:
  sweep_interval: 30s

sink_retry:
  max_attempts: 5
  base_backoff: 1s
  max_backoff: 30s

rate_limits:
  publish:     { rate: 100, per: 1s, burst: 30 }
  subscribe:   { rate: 20,  per: 1s, burst: 10 }
  token_issue: { rate: 5,   per: 1s }

observability:
  metrics_bearer_token: "${PARSEC_METRICS_TOKEN}"
  trusted_proxies:
    - 10.0.0.0/8

sinks:
  alerts-email:
    kind: email
    smtp_addr: smtp.example.com:587
    from: ops@example.com
    auth_user: ${SMTP_USER}
    auth_pass: ${SMTP_PASS}
  ops-slack:
    kind: slack
    webhook_url: ${SLACK_WEBHOOK}
```

Full reference: [examples/config/parsec.yaml](examples/config/parsec.yaml)
and [docs/src/ops/config.md](docs/src/ops/config.md).

---

## Operating modes

| Mode | What you get | How |
|---|---|---|
| **Single-node** | In-memory channel registry, in-memory keyring (or file with `--state-dir`), in-memory DLQ, in-memory rate limiter | Default. No Redis required. |
| **Clustered** | Redis-backed broker (centrifuge.RedisBroker) + presence, Redis HASH channel registry with pub/sub event bus, Redis-watched keyring, Redis Streams DLQ, Redis sliding-window rate limiter | Set `Options.RedisClient` (or `--redis-addr`). Every subsystem switches automatically. |
| **Multi-region** | Cross-region keyring sync via pub/sub bridge, manifest peer list, region label on metrics/logs | Run `parsec-keys-sync` between regions; set `Options.Region` and `Options.Peers`. |

---

## Surfaces

| Surface | Use case | Scope |
|---|---|---|
| Go library | Embedded in your service | Full |
| CLI | Operator control + scripts | Full (client-scoped) |
| Twirp JSON RPC | External clients | Full (client-scoped) |
| WebSocket | Browser + service clients | Subscribe / publish |
| WebTransport (HTTP/3) | Low-latency browser clients | Subscribe / publish |
| Admin UI (`/admin`) | Operator dashboard | Read-only, opt-in |
| Prometheus (`/metrics`) | Scrape | Read-only, bearer-gated |

---

## Auth model

Three HMAC-SHA256 JWT token types, all `kid`-tagged:

| Type | Default TTL | Used for |
|---|---|---|
| Access  | 5 min | Websocket connect + subscribe to listed channels |
| Refresh | min(channel TTL, 1h) | Exchange at `tokens refresh` for a new access |
| Mgmt    | 24h | `Authorization: Bearer` on the management RPC |

Tokens carry an exact-match channel list (`chs`) **and** optional
pattern-based scopes:

```json
{
  "sub": "ops-bot",
  "chs": ["public:webapp.system.status"],
  "scopes": [
    {"pattern": "public:webapp.*.metrics", "verbs": ["subscribe"]},
    {"pattern": "private:webapp.user.**", "verbs": ["publish"], "deny": true}
  ]
}
```

Verbs: `subscribe`, `publish`, `manage`. Patterns use the channel grammar
plus `*` (single segment) and trailing `**` (multi-segment). Evaluation
is **deny → chs → allow**; any matching `deny` wins.

When `Options.OIDCConfig` is non-nil, an OIDC composite verifier accepts
IdP-issued ID tokens as mgmt bearers — group claims map to scope grants,
so role-based access lives in the IdP. `parsec login oidc` runs the
device-code flow and persists the token at `~/.parsec/credentials`.

`Manifest` and `RefreshToken` skip the bearer gate; every other RPC
requires a valid mgmt token signed by a non-retired key.

---

## Zero-downtime key rotation

The keyring file lives at `<state-dir>/keyring.json`. Procedure:

```bash
parsec keys generate                       # new kid joins as verify-only
parsec keys promote <new-kid>              # new tokens use new-kid; old still verify
NEW_BEARER=$(parsec tokens mgmt | jq -r .payload.mgmt_token)
export PARSEC_TOKEN="$NEW_BEARER"          # switch operator bearer
# wait the longest in-flight token TTL (default 24h for mgmt)
parsec keys retire <old-kid>               # old key stops verifying
```

Reload triggers: SIGHUP, `parsec keys reload`, or the mtime-poll watcher
(5s default). Full runbook including break-glass:
[docs/src/ops/key-rotation.md](docs/src/ops/key-rotation.md).

---

## Sink reliability

Every sink in `Options.Sinks` is wrapped in `sinks.Retrier` at
construction time (idempotent). Defaults: 5 attempts, exponential
backoff 1s → 30s, 20% jitter — override with `Options.SinkRetry` or
`Options.PerSinkRetry["<name>"]`. Sinks classify errors by wrapping with
`sinks.Transient(err)` / `sinks.Terminal(err)`.

When retries exhaust, the `Retrier` pushes a `DLQItem` and returns nil —
the caller only sees `PARSEC_SINK_DLQ_OVERFLOW` if the DLQ push itself
fails.

| CLI | What |
|---|---|
| `parsec dlq list` | One row per parked item |
| `parsec dlq count` | Per-sink counters |
| `parsec dlq discard <id>` | Drop an item |
| `parsec dlq replay <id>` | Re-enqueue through the retrier |

Backends: `sinks.MemoryDLQ` (single-node) or `sinks.RedisDLQ` (one stream
per sink, `parsec:dlq:<sink>`).

---

## Observability

| Stream | Where | Notes |
|---|---|---|
| Prometheus metrics | `/metrics` | Bearer-gated when `MetricsBearerToken` set. Cardinality budget: visibility + result enums + bounded sink/method names. No channel names or subjects in labels. |
| OpenTelemetry traces | OTLP HTTP | Off by default; set `Options.OTLPEndpoint`. No-op tracer is zero-alloc. |
| Structured access logs | slog INFO | One line per HTTP request with `method/path/status/duration_ms/request_id/remote_addr/bearer_subject/trace_id`. XFF-aware via `Options.TrustedProxies`. Token contents are never logged. |

A starter Grafana dashboard lives at
[`examples/grafana/`](examples/grafana/). Metric reference:
[docs/src/ops/observability.md](docs/src/ops/observability.md).

---

## Documentation map

The full mdBook is under [`docs/`](docs/). Top of mind:

| Section | Pages |
|---|---|
| **Getting started** | [installation](docs/src/getting-started/installation.md), [quickstart](docs/src/getting-started/quickstart.md), [Docker](docs/src/getting-started/docker.md), [browser client](docs/src/getting-started/browser-client.md) |
| **Channels** | [naming](docs/src/channels/naming.md), [public](docs/src/channels/public.md), [private](docs/src/channels/private.md), [TTL](docs/src/channels/ttl.md), [ACL scopes](docs/src/channels/acl.md) |
| **CLI** | [serve](docs/src/cli/serve.md), [channels](docs/src/cli/channels.md), [publish](docs/src/cli/publish.md), [subscribe](docs/src/cli/subscribe.md), [tokens](docs/src/cli/tokens.md), [keys](docs/src/cli/keys.md), [dlq](docs/src/cli/dlq.md), [login](docs/src/cli/login.md) |
| **Library** | [overview](docs/src/library/overview.md), [options](docs/src/library/options.md), [custom sinks](docs/src/library/sinks.md), [testing with parsectest](docs/src/library/testing.md) |
| **Ops** | [deployment](docs/src/ops/deployment.md), [config file](docs/src/ops/config.md), [observability](docs/src/ops/observability.md), [DLQ](docs/src/ops/dlq.md), [rate limiting](docs/src/ops/rate-limiting.md), [key rotation](docs/src/ops/key-rotation.md), [OIDC](docs/src/ops/oidc.md), [admin UI](docs/src/ops/admin-ui.md), [multi-region](docs/src/ops/multi-region.md), [troubleshooting](docs/src/ops/troubleshooting.md) |
| **Internals** | [architecture](docs/src/internals/architecture.md), [broker](docs/src/internals/broker.md), [channel manager](docs/src/internals/channels.md) |
| **RPC** | [Twirp service](docs/src/rpc/twirp.md) |

Build locally with `make docs` (mdBook required).

---

## Repo layout

```
parsec.go                 # library facade — embed this
parsectest/               # test helpers (`parsectest.New(t)`, miniredis variant)
auth/                     # HMAC + OIDC verifiers, KeyRing, scopes, claims
broker/                   # centrifuge.Node wrapper (Redis broker swap, presence)
channels/                 # naming grammar, Manager, EventBus, Store impls
sinks/                    # Sink interface + email/slack/webhook + Retrier + DLQ
ratelimit/                # Limiter interface + memory + Redis sliding-window
service/                  # surface-agnostic business logic
rpc/                      # Twirp JSON wire (generated from service.proto)
descriptor/               # manifest envelope (CLI --json, /manifest)
errors/                   # coded errors (PARSEC_*)
internal/admin/           # embedded SPA at /admin
internal/cli/             # CLI subcommands (urfave/cli/v3)
internal/config/          # YAML loader + Resolved → Options
internal/metrics/         # Prometheus collectors
internal/rpcclient/       # CLI adapter onto the Twirp JSON client
internal/server/          # HTTP mux: twirp + ws + wt + sse + admin + healthz
internal/tracing/         # OTel tracer (no-op default)
cmd/parsec/               # main.go — thin entry point
cmd/parsec-keys-sync/     # multi-region keyring pub/sub bridge daemon
Dockerfile                # multi-stage; ships gcr.io/distroless/static
docker-compose.yml        # parsec + redis local stack
docs/                     # mdBook source
examples/                 # runnable scenarios + reference YAML + Grafana + browser
```

---

## Build, test, lint

```bash
make build      # bin/parsec
make test       # go test -race ./...
make lint       # go vet + staticcheck
make cover      # coverage profile
make proto      # regenerate rpc/ from service.proto (requires protoc-gen-twirp)
make docs       # mdBook build
```

CGO is disabled. Pinned: Go 1.26.1, centrifuge v0.36.0,
urfave/cli/v3 v3.9.0, afero v1.15.0.

---

## License

MIT — see [LICENSE](LICENSE).
