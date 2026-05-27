# parsec dlq

Inspect and operate on the sink dead-letter queue. Every parked item is
a `DLQItem` carrying the failing sink name, the original message, the
last error, and a retry count. The backend is `sinks.MemoryDLQ`
(single-node) or `sinks.RedisDLQ` (Redis Streams, one stream per sink at
`parsec:dlq:<sink>`).

## Flags

| Flag | Env | Default |
|---|---|---|
| `--server` | `PARSEC_SERVER` | `http://localhost:8000` |
| `--token`  | `PARSEC_TOKEN`  | (empty — required) |

## dlq list

```bash
parsec dlq list <sink-name> --limit 50
```

Returns the newest items first. Each row carries:

| Field | Notes |
|---|---|
| `id` | Opaque DLQ identifier. Stable across replays. |
| `sink` | The sink name (matches the Sinks registry key). |
| `channel` | Channel the publication targeted. |
| `recipient` | Sink-specific recipient JSON (decoded via `sinks.RecipientDecoder`). |
| `message` | The original `parsec.Message`. |
| `last_error` | Most recent error string. |
| `attempts` | Number of retries before parking. |
| `parked_at` | RFC3339 timestamp. |

## dlq count

```bash
parsec dlq count <sink-name>
```

Returns the total parked count for the named sink. Useful to wire into
alerting — non-zero is the operator's signal to investigate.

## dlq discard

```bash
parsec dlq discard <id>
```

Permanently remove an item. The publication is lost; no further
delivery attempt is made.

## dlq replay

```bash
parsec dlq replay <id>
```

Re-enqueue the item through the original sink's `Retrier`. The full
retry policy runs again — if the underlying failure persists, the item
returns to the DLQ with an incremented attempts count.

`replay` requires the sink to implement `sinks.RecipientDecoder` so the
opaque recipient JSON can be rebuilt into the typed value. Built-in
sinks (email / slack / webhook) all satisfy this; custom sinks must
implement the interface or `replay` returns `PARSEC_SINK_RECIPIENT_DECODE`.

## Operator runbook

See [docs/src/ops/dlq.md](../ops/dlq.md) for the failure-mode catalog,
retention policy, and how to wire DLQ count into alerting.
