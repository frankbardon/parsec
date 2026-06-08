# Observability — Parsec

Load when adding a subsystem with internal state, touching metrics,
tracing, access logs, or the telemetry aggregator.

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
why none is needed. See
[docs/src/ops/observability.md](../../docs/src/ops/observability.md)
for the metric reference and Grafana starter dashboard.

## Telemetry aggregator (`telemetry/`)

`telemetry.Aggregator` composes per-source dashboard stats into a
single `Snapshot` served as JSON at `/parsec/metrics` (via
`Options.TelemetryHandler`). The aggregator also supports:

- **Declarative alert rules** via `Aggregator.WithAlerts([]AlertRule)`.
  Each rule has a `Name`, `Severity` (`info`/`warning`/`critical`),
  `Description`, and `Condition func(Snapshot) bool`. Rules fire on
  every Snapshot and surface in `Snapshot.Alerts`. Validation runs at
  `WithAlerts` time — duplicate names, empty names, nil conditions,
  or unknown severities abort boot.
- **Prometheus text exposition** via `Aggregator.PrometheusHandler()`.
  Re-aggregates on every scrape and renders each Snapshot field as a
  `parsec_telemetry_*` gauge; firing alerts surface as
  `parsec_telemetry_alerts_firing{alert,severity}`. Cardinality budget
  same as `/metrics`: only bounded labels (`pattern`, `aspect`,
  `alert`, `severity`) escape.

The aggregator is opt-in — embedders construct it and wire
`Options.TelemetryHandler`. See
[docs/src/ops/telemetry-alerts.md](../../docs/src/ops/telemetry-alerts.md).
