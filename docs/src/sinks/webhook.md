# Webhook sink

Posts a JSON envelope to a configurable HTTP endpoint. Use this when
the downstream consumer is another service rather than a person —
internal scoreboards, downstream workers, audit sinks.

```go
import (
    "github.com/frankbardon/parsec"
    "github.com/frankbardon/parsec/sinks/webhook"
)

w, _ := webhook.New(webhook.Config{
    URL: "https://hooks.internal.example.com/parsec",
    Headers: map[string]string{
        "X-Parsec-Source": "prod",
        "Authorization":   "Bearer " + os.Getenv("DOWNSTREAM_TOKEN"),
    },
})

p, _ := parsec.New(parsec.Options{})
p.Sinks().Register(w)
```

Then:

```go
_, _ = p.PublishOrSink(ctx,
    "private:webapp.user.42.downloads",
    []byte(`{"status":"ready"}`),
    "webhook",
    webhook.Recipient{URL: ""}, // use the configured URL
    parsec.Message{
        Subject: "download.ready",
        Body:    `{"user_id":42}`,
        Metadata: map[string]string{"trace_id": "abc"},
    })
```

## Configuration

`webhook.Config`:

| Field | Required | Notes |
|---|---|---|
| `URL` | yes | Default POST target. Used when the recipient does not override it. |
| `Headers` | no | Static headers added to every request. Useful for an auth header or a fixed `X-Parsec-Source`. |
| `HTTPClient` | no | Defaults to `http.DefaultClient`. Inject your own for timeouts, tracing, retries. |

## Recipient

`webhook.Recipient`:

```go
type Recipient struct {
    URL string
}
```

`URL == ""` (or passing `nil` / a non-`webhook.Recipient` value) means
"use the configured URL". A non-empty `URL` overrides the destination
on a per-call basis — useful when one sink instance fans out to
multiple downstream services that share a common header set.

## Payload shape

The sink marshals a fixed envelope:

```json
{
  "subject":  "download.ready",
  "body":     "{\"user_id\":42}",
  "metadata": {"trace_id": "abc"}
}
```

Fields with zero values are omitted. The wire `Content-Type` is
`application/json`. Configured headers are set after the
`Content-Type`, so a config that overrides `Content-Type` wins.

## Failure semantics

| Condition | Returned code |
|---|---|
| Missing `URL` at construction | `PARSEC_SINK_CONFIG_INVALID` |
| Marshalling the envelope fails | `PARSEC_INTERNAL` (very rare) |
| `http.Client.Do` returns an error | `PARSEC_SINK_UNAVAILABLE` |
| Downstream returns >= 300 status | `PARSEC_SINK_UNAVAILABLE` (carries the status line) |

The sink does NOT retry, NOT queue, and NOT batch. It's a thin
forwarder. If downstream is flaky, wrap it.

## Patterns

- One sink per downstream service is the easy default — name them
  `"webhook-audit"`, `"webhook-scoreboard"`, etc.
- One sink + per-call URL override works when the destinations share
  headers (auth, tracing) but differ in path.
- For at-least-once semantics, queue the message yourself (e.g. in
  Kafka or a database table) and have a worker call `Send` with a
  retry loop. Parsec sinks are best-effort, not durable.

## See also

- [Custom sinks](../library/sinks.md) — write your own.
- [Email sink](email.md) and [Slack sink](slack.md).
