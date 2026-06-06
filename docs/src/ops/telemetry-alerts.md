# Telemetry Alerts & Prometheus Exposition

The `telemetry` package aggregates per-source dashboard metrics into a
single `Snapshot` served as JSON at `/parsec/metrics`. This page covers
two extensions that operators commonly want on top:

1. **Declarative alert rules** that fire when an aggregate value
   crosses a threshold.
2. **Prometheus text exposition** of the same Snapshot so an external
   Prometheus + AlertManager pair can scrape it alongside the
   process-level `/metrics` registry.

The existing JSON view at `/parsec/metrics` is unchanged — alerts
attach to the same `Aggregator` and surface in `Snapshot.Alerts`.

## Alert rules

An `AlertRule` is a name, severity, description, and a `Condition`
closure that receives the freshly computed `Snapshot` and returns
true when the rule should fire.

```go
import "github.com/frankbardon/parsec/telemetry"

agg := telemetry.New(channelSrc, envSrc, tokenSrc, cacheSrc)
agg, err := agg.WithAlerts([]telemetry.AlertRule{
    {
        Name:        "cache_cold",
        Severity:    telemetry.SeverityWarning,
        Description: "Cache hit rate dropped below 80%",
        Condition: func(s telemetry.Snapshot) bool {
            return s.Cache.HitRatePct < 80
        },
    },
    {
        Name:        "history_pressure",
        Severity:    telemetry.SeverityCritical,
        Description: "History buffers above 90% utilization",
        Condition: func(s telemetry.Snapshot) bool {
            return s.History.BufferUtilizationPct > 90
        },
    },
})
if err != nil {
    log.Fatalf("alert rules: %v", err)
}

opts := parsec.Options{TelemetryHandler: agg.Handler()}
```

`WithAlerts` runs `ValidateAlertRules` first — empty names, unknown
severities, nil `Condition` closures, and duplicate names abort the
boot (matching the rest of parsec's "fail loud on misconfiguration"
posture).

### Severities

| Severity | Meaning |
|---|---|
| `info` | The condition is unusual but does not require action. |
| `warning` | Investigate; the system is still serving traffic. |
| `critical` | Immediate action — user impact imminent or active. |

### JSON output

`Snapshot.Alerts` is omitted from the JSON when empty and rendered as
an array of firing rules when not:

```json
{
  "channels": {"total_active": 12},
  "tokens":   {"active_count": 4810},
  "cache":    {"hit_rate_pct": 73.2},
  "alerts": [
    {
      "name": "cache_cold",
      "severity": "warning",
      "description": "Cache hit rate dropped below 80%"
    }
  ],
  "at": "2026-06-06T19:00:00Z"
}
```

## Prometheus exposition

`Aggregator.PrometheusHandler()` returns an `http.Handler` that
re-aggregates on every scrape and writes the Snapshot in Prometheus
text exposition format (version 0.0.4). Mount it next to the JSON
handler:

```go
mux := http.NewServeMux()
mux.Handle("/parsec/metrics", agg.Handler())                  // JSON
mux.Handle("/parsec/telemetry/metrics", agg.PrometheusHandler()) // Prometheus
```

Operators can also wire it as `Options.TelemetryHandler` directly
when they only want the Prometheus view.

### Metric reference

| Metric | Type | Labels |
|---|---|---|
| `parsec_telemetry_channels_active` | gauge | — |
| `parsec_telemetry_channels_by_pattern` | gauge | `pattern` |
| `parsec_telemetry_envelope_rate_per_second` | gauge | — |
| `parsec_telemetry_envelopes_by_aspect` | gauge | `aspect` |
| `parsec_telemetry_presence_total_users` | gauge | — |
| `parsec_telemetry_presence_total_agents` | gauge | — |
| `parsec_telemetry_presence_average_per_channel` | gauge | — |
| `parsec_telemetry_history_buffered` | gauge | — |
| `parsec_telemetry_history_buffer_utilization_pct` | gauge | — |
| `parsec_telemetry_tokens_issued_last_hour` | gauge | — |
| `parsec_telemetry_tokens_revoked_last_hour` | gauge | — |
| `parsec_telemetry_tokens_active` | gauge | — |
| `parsec_telemetry_cache_hit_rate_pct` | gauge | — |
| `parsec_telemetry_cache_size_bytes` | gauge | — |
| `parsec_telemetry_cache_size_entries` | gauge | — |
| `parsec_telemetry_cache_evictions_last_hour` | gauge | — |
| `parsec_telemetry_alerts_firing` | gauge | `alert`, `severity` |

`parsec_telemetry_alerts_firing` only emits samples for rules that are
currently firing (value `1`); a rule that no longer fires simply
disappears from the scrape. Operators who want a "0 when not firing"
exposition shape should add their own AlertManager rule that uses
`absent_over_time`.

### Cardinality discipline

Only bounded labels escape the handler:

- `pattern` — the configured channel grammar pattern. The embedder
  controls this set when constructing the `ChannelLister`.
- `aspect` — the envelope kind reported by `EnvelopeCounters.Observe`.
  Constrain it at the call site (data / cursor / heartbeat / etc.).
- `alert` — the `AlertRule.Name`. Operators own the rule list.
- `severity` — one of `info`, `warning`, `critical`.

Channel names, subject IDs, and bearer tokens are NEVER labels —
matching the existing `/metrics` cardinality contract.

## AlertManager wiring

A starter `prometheus.yml` snippet:

```yaml
scrape_configs:
  - job_name: parsec-telemetry
    static_configs:
      - targets: ["parsec.internal:8000"]
    metrics_path: /parsec/telemetry/metrics

rule_files:
  - parsec_telemetry_rules.yml
```

A matching `parsec_telemetry_rules.yml`:

```yaml
groups:
  - name: parsec-telemetry
    rules:
      - alert: ParsecCacheCold
        expr: parsec_telemetry_alerts_firing{alert="cache_cold"} == 1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Parsec cache hit rate below threshold"
      - alert: ParsecHistoryPressure
        expr: parsec_telemetry_alerts_firing{alert="history_pressure"} == 1
        for: 2m
        labels:
          severity: critical
```

Because the alert metric already encodes the rule semantics, the
Prometheus rule simply checks for the gauge's presence. Operators who
want richer alerting can run additional `expr` rules over the
underlying gauges (`parsec_telemetry_cache_hit_rate_pct < 80`) instead
of the firing gauge.
