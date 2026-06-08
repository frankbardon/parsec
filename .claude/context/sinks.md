# Sink reliability — Parsec

Load when adding a new sink, changing retry policy, or touching the
DLQ.

`PublishOrSink` retries transient sink failures and lands terminal
failures in a dead-letter queue. The contract:

1. Every sink in `parsec.Options.Sinks` is wrapped in a `sinks.Retrier`
   at construction time. The wrap is idempotent — wrapping an
   already-wrapped sink is a no-op.
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

See [docs/src/ops/dlq.md](../../docs/src/ops/dlq.md) for the
operator-side runbook.
