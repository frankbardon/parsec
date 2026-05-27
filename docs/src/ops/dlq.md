# Sink dead-letter queue (DLQ)

Sinks transparently retry transient failures with exponential backoff +
jitter. Anything that survives the retry budget — or is classified as
terminal from the first call — lands in the DLQ. An operator inspects,
replays, or discards items from there.

## Backends

| Backend     | When it activates                                  | Persistence              |
|-------------|----------------------------------------------------|--------------------------|
| `memory`    | `Options.RedisClient == nil`                       | Process-local; lost on restart |
| `redis`     | `Options.RedisClient != nil` (or `--redis-addr`)   | Redis Streams, one stream per sink (`parsec:dlq:<sink>`) |

The backend in use is reported in the `dlq_backend` field of
`parsec --json`.

## Retry policy

Default config (overridable via `Options.SinkRetry` or
`Options.PerSinkRetry`):

| Field            | Default |
|------------------|---------|
| `MaxAttempts`    | 5       |
| `BaseBackoff`    | 1s      |
| `MaxBackoff`     | 30s     |
| `JitterFraction` | 0.2     |

Backoff per attempt is `BaseBackoff * 2^(attempt-2)` capped at
`MaxBackoff`, multiplied by `uniform(1 - JitterFraction, 1 + JitterFraction)`
to break thundering herds.

Set `MaxAttempts = 1` to disable retries (terminal-or-DLQ on first error).

## CLI

```
parsec dlq list <sink>            # newest first
parsec dlq count <sink>           # XLEN
parsec dlq discard <id>           # XDEL
parsec dlq replay <id>            # re-run through the original sink
```

Every command honors `--server` and `--token` (or `PARSEC_SERVER` /
`PARSEC_TOKEN`).

### Inspecting items

```
$ parsec dlq list email --limit 10
{
  "format_version": "1.0",
  "kind": "parsec.dlq.list",
  "payload": {
    "sink": "email",
    "items": [
      {
        "id": "1741310221012-0",
        "sink": "email",
        "at": "2026-05-22T18:30:21Z",
        "subject": "Order shipped",
        "body": "Your package is on the way.",
        "attempts": 5,
        "last_error": "smtp send: 451 4.4.5 Temporary error",
        "recipient": {"address": "alice@example.com"}
      }
    ]
  }
}
```

### Replaying

Replay reruns the stored item through the original sink. If the sink is
wrapped by a `Retrier` (the default), the replay itself retries up to
`MaxAttempts`. If it fails again it lands back in the DLQ as a *new*
item — the original entry stays in place so you can compare attempt
counts.

```
$ parsec dlq replay 1741310221012-0
{ "kind": "parsec.dlq.replayed", "payload": {"id": "1741310221012-0"} }
```

### Discarding

`discard` is final. Use it when the payload is irrelevant or the failure
was diagnosed and the item is unrecoverable.

```
$ parsec dlq discard 1741310221012-0
```

## RPC

| Method        | Request                              | Response               |
|---------------|--------------------------------------|------------------------|
| `DlqList`     | `{sink, limit}`                      | `{items[]}`            |
| `DlqCount`    | `{sink}`                             | `{count}`              |
| `DlqDiscard`  | `{id}`                               | `Empty`                |
| `DlqReplay`   | `{id}`                               | `Empty`                |

All methods require a mgmt bearer.

## Stream growth & retention

The Redis backend uses `XADD MAXLEN ~ 10000` by default — old entries are
trimmed approximately when the stream grows past the cap. Override via
`RedisDLQ.WithMaxLen(n)` for tighter or looser retention.

## Error taxonomy

| Code                       | Meaning                                              |
|----------------------------|------------------------------------------------------|
| `PARSEC_SINK_UNAVAILABLE`  | Transient sink failure (retry will run)              |
| `PARSEC_SINK_DLQ_OVERFLOW` | Retries exhausted **and** DLQ push itself failed     |
| `PARSEC_SINK_DLQ_NOT_FOUND`| Replay/Discard referenced an unknown id              |
| `PARSEC_SINK_CONFIG_INVALID` | Sink misconfigured at registration time            |

`PublishOrSink` returns `err == nil` when the DLQ accepted the failed
payload — the failure has been recorded. It returns an error only when
the DLQ itself failed (overflow / Redis unreachable).
