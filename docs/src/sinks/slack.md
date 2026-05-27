# Slack sink

Posts to a Slack incoming-webhook URL. One sink instance per
destination channel; the webhook URL fixes which Slack channel the
message lands in.

```go
import (
    "github.com/frankbardon/parsec"
    "github.com/frankbardon/parsec/sinks/slack"
)

s, _ := slack.New(slack.Config{
    WebhookURL: os.Getenv("PARSEC_SLACK_WEBHOOK"),
})

p, _ := parsec.New(parsec.Options{})
p.Sinks().Register(s)
```

Then:

```go
_, _ = p.PublishOrSink(ctx,
    "private:agent.analysis.42.failures",
    []byte(`{"job":42,"err":"timeout"}`),
    "slack",
    nil, // recipient is ignored
    parsec.Message{
        Subject: "Job 42 failed",
        Body:    "timeout while fetching artifacts",
    })
```

## Configuration

`slack.Config`:

| Field | Required | Notes |
|---|---|---|
| `WebhookURL` | yes | The full Slack incoming-webhook URL. |
| `HTTPClient` | no | Defaults to `http.DefaultClient`. Override to inject timeouts / tracing. |

## Recipient

`slack.Recipient` is an empty struct — the channel is determined by the
webhook URL itself. Callers may pass `nil` to `Send`; the sink ignores
the value.

If you need to post to multiple Slack channels, register multiple sink
instances under different names (e.g. `"slack-alerts"`,
`"slack-deploys"`) and pick the right one per call.

## Payload shape

The sink posts a JSON body like:

```json
{
  "text": "*Job 42 failed*\ntimeout while fetching artifacts",
  "metadata": {"trace_id": "..."}
}
```

`Subject` becomes a bold prefix on `text`; an empty `Subject` posts the
body alone. `Metadata` is attached verbatim so it appears in Slack
webhook metadata where supported.

## Failure semantics

| Condition | Returned code |
|---|---|
| Missing `WebhookURL` at construction | `PARSEC_SINK_CONFIG_INVALID` |
| `http.Client.Do` returns an error | `PARSEC_SINK_UNAVAILABLE` |
| Slack returns a >= 300 status | `PARSEC_SINK_UNAVAILABLE` (carries the status line) |

The sink does not retry. Slack rate-limits webhooks; downstream
clients that hit `429` should back off — the sink does not.

## Common gotchas

- Slack webhooks are long-lived but per-channel. If you change the
  destination Slack channel, mint a new webhook and re-register.
- Slack does not signal whether the human at the keyboard saw the
  message — `Send` returning nil means "Slack accepted the post", not
  "the user saw it". Real delivery confirmation needs the Slack Web
  API and a different sink.

## See also

- [Custom sinks](../library/sinks.md) — write your own.
- [Email sink](email.md) and [Webhook sink](webhook.md) — the other
  reference implementations.
