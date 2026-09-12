# Observability

Every Parsec deployment exposes three streams: **Prometheus metrics** at
`/metrics`, **OpenTelemetry traces** to an OTLP collector, and
**structured access logs** through `slog`.

The defaults are operator-friendly: metrics are always on; tracing is
off until you set an endpoint; the access log uses the `Logger` you
pass into `parsec.Options` (or `slog.Default()` if none).

## Metrics

### Scraping `/metrics`

The route is mounted on the same HTTP mux as everything else. Set
`Options.MetricsBearerToken` to require `Authorization: Bearer <token>`
on every scrape; leave it empty to run unguarded (firewall the route
at the network layer in that case).

```bash
curl -H "Authorization: Bearer $METRICS_TOKEN" \
     http://parsec.internal:8000/metrics
```

The registry includes Go runtime + process collectors by default. To
share a registry with the rest of your service, pass
`Options.MetricsRegistry`.

### Metric reference

| Metric | Type | Labels | Source |
|---|---|---|---|
| `parsec_publishes_total` | counter | `channel_visibility`, `result` | every `Publish` call |
| `parsec_publish_duration_seconds` | histogram | `channel_visibility` | every `Publish` call |
| `parsec_subscribers_active` | gauge | `channel_visibility` | broker subscribe / unsubscribe hooks |
| `parsec_channels_active` | gauge | `state`, `visibility` | sweeper, after every tick |
| `parsec_token_verifications_total` | counter | `type`, `result` | every `Verifier.Verify` |
| `parsec_sink_attempts_total` | counter | `sink`, `result` | every sink `Send` |
| `parsec_sink_duration_seconds` | histogram | `sink` | every sink `Send` |
| `parsec_dlq_size` | gauge | `sink` | periodic scrape (sweep interval) |
| `parsec_key_rotations_total` | counter | `action` | `GenerateKey` / `PromoteKey` / `RetireKey` |
| `parsec_keyring_backend` | gauge | `backend` | boot; `1` for the active backend, `0` for the others |
| `parsec_keyring_version` | gauge | — | the shared store's revision counter; `-1` for backends without one |
| `parsec_keyring_active_key_age_seconds` | gauge | — | refreshed on the keyring poll interval |
| `parsec_keyring_watch_up` | gauge | — | `1` while the keyring watcher runs, `0` between restarts |
| `parsec_keyring_reconcile_errors_total` | counter | — | failed reconcile reads and dropped watch subscriptions |
| `parsec_rpc_requests_total` | counter | `method`, `status` | HTTP middleware (last URL segment) |
| `parsec_rpc_duration_seconds` | histogram | `method` | HTTP middleware |

`result` values are `success` or `failure`. `channel_visibility` is
`public` or `private` (or `unknown` for the rare race where a name has
not been parsed). `action` for `key_rotations_total` is one of
`generated`, `promoted`, `retired`. `backend` for `keyring_backend` is one
of `file`, `redis`, `ephemeral`, `external`.

### Keyring health

The keyring gauges answer three questions a multi-node deployment cannot
otherwise see.

**Do the nodes agree?** `parsec_keyring_version` is the shared store's
revision. Every node should report the same number; one stuck lower has
not picked up a rotation.

```yaml
- alert: ParsecKeyringDiverged
  expr: max(parsec_keyring_version) - min(parsec_keyring_version) > 0
  for: 2m
  annotations:
    summary: Parsec nodes disagree about the keyring revision
```

**Can each node still see rotations?** A node with
`parsec_keyring_watch_up == 0` for more than a moment is not receiving
them, and `parsec_keyring_reconcile_errors_total` climbing means its reads
are failing too.

```yaml
- alert: ParsecKeyringWatchDown
  expr: parsec_keyring_watch_up == 0
  for: 5m
```

**Is a rotation overdue?** `parsec_keyring_active_key_age_seconds` is the
only thing that makes a key nobody rotated visible. Pick a threshold from
your own policy — the runbook is in
[key rotation](key-rotation.md).

```yaml
- alert: ParsecSigningKeyOverdue
  expr: parsec_keyring_active_key_age_seconds > 60 * 60 * 24 * 90
  annotations:
    summary: Active parsec signing key is older than 90 days
```

`parsec_keyring_backend` is worth an alert of its own if you ever deploy
with Redis configured: `parsec_keyring_backend{backend="ephemeral"} == 1`
in production means tokens will not survive the next restart.

### Cardinality contract

Channel names, subject IDs, and bearer tokens are **never** labels.
The label set is bounded by:

- `channel_visibility` ∈ {`public`, `private`, `unknown`}
- `result` ∈ {`success`, `failure`}
- `type` ∈ {`access`, `refresh`, `mgmt`, `unknown`}
- `sink` — bounded by the registered sink registry (3 in the default build)
- `state` ∈ {`open`, `closed`, `deleted`}
- `action` ∈ {`generated`, `promoted`, `retired`}
- `backend` ∈ {`file`, `redis`, `ephemeral`, `external`}

Key ids are deliberately **not** labels. A `kid` label would grow without
bound across rotations; `parsec_keyring_version` conveys the same
per-node divergence signal at fixed cardinality.
- `method` — the last URL segment of a `/twirp/parsec.ParsecService/`
  path; anything outside the prefix is collapsed to the literal
  `non_rpc`

If you add a new subsystem with internal state, add a metric or
document why none is needed (see CLAUDE.md Update Demand).

## Tracing

Set `Options.OTLPEndpoint` to enable the OTLP HTTP exporter:

```go
parsec.Options{
    OTLPEndpoint: "otel-collector.observability:4318",
}
```

When the endpoint is empty (the default), `internal/tracing.NewTracer`
returns a no-op tracer that performs no allocations. There is **no
runtime cost** to leaving tracing off.

The service name is baked in as `parsec`. The exporter uses insecure
transport — terminate TLS at your collector or run a sidecar.

Each RPC request gets a server span; sub-spans for token verification,
broker publish, and sink delivery are emitted by the corresponding
subsystems. Trace context propagates via standard W3C headers — Parsec
is a participant, not an originator.

## Access logs

The HTTP surface emits one `slog` INFO line per request:

```json
{
  "time": "2026-05-22T14:03:11.123Z",
  "level": "INFO",
  "msg": "http_request",
  "method": "POST",
  "path": "/twirp/parsec.ParsecService/Publish",
  "status": 200,
  "duration_ms": 7,
  "remote_addr": "203.0.113.42",
  "request_id": "f9e3c0d1...",
  "bearer_subject": "ops-alice",
  "trace_id": "9c5f1a..."
}
```

Failed token verifications emit a WARN line with a `PARSEC_AUTH_*`
code; the token contents are **never** logged:

```json
{
  "level": "WARN",
  "msg": "auth_failure",
  "code": "PARSEC_AUTH_EXPIRED",
  "request_id": "f9e3c0d1..."
}
```

### Trusting `X-Forwarded-For`

`Options.TrustedProxies` is the list of CIDRs Parsec will honor an
`X-Forwarded-For` chain from. If the immediate TCP peer is not in the
list, XFF is ignored and the TCP peer is logged. This prevents any
unauthenticated client from spoofing their IP by setting a header.

```go
_, lb, _ := net.ParseCIDR("10.0.0.0/8")
opts := parsec.Options{
    TrustedProxies: []net.IPNet{*lb},
}
```

### Request IDs

The middleware honors `X-Request-ID` when the client provides one and
generates a fresh 24-character hex value otherwise. The chosen ID is
echoed back in the response so clients can correlate.

## Grafana dashboard

A starter dashboard JSON lives at
[`examples/grafana/parsec-overview.json`](https://github.com/frankbardon/parsec/blob/main/examples/grafana/parsec-overview.json).
It includes panels for:

- publish rate by visibility
- sink success ratio
- active channel inventory
- token verification failures
- key rotation activity

Import via the Grafana UI (Dashboards → Import → Upload JSON).

## Telemetry alerts and Prometheus exposition

The aggregated `/parsec/metrics` JSON view can also surface declarative
alert firings and a parallel Prometheus text exposition for external
scraping. See
[Telemetry Alerts & Prometheus Exposition](telemetry-alerts.md) for
the rule shape, metric reference, and AlertManager wiring.
