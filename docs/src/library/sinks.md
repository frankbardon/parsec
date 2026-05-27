# Custom sinks

A "sink" is what Parsec delivers a message to when the realtime channel
has nobody listening. The `sinks.Sink` interface is two methods; the
registry is a `map[string]Sink`.

```go
type Sink interface {
    Name() string
    Send(ctx context.Context, to Recipient, m Message) error
}
```

Ship a custom sink by implementing the interface, registering it on
`*parsec.Parsec`, and naming it in your `PublishOrSink` calls.

## The interface

`sinks/sink.go`:

```go
type Message struct {
    Subject  string
    Body     string
    Metadata map[string]string
}

type Recipient any // typed per-sink

type Sink interface {
    Name() string
    Send(ctx context.Context, to Recipient, m Message) error
}
```

`Recipient` is `any` because each sink defines its own target shape:

- Email sinks expect a struct with an address.
- Slack sinks usually ignore the recipient (the webhook URL fixes the
  destination).
- Webhook sinks may carry a per-call URL override.

Pick a typed `Recipient` struct in your sink package and do the
type-assert inside `Send`. Reference: `sinks/email`, `sinks/slack`,
`sinks/webhook`.

## Implementation skeleton

```go
package mysink

import (
    "context"

    "github.com/frankbardon/parsec/errors"
    "github.com/frankbardon/parsec/sinks"
)

type Config struct{ /* ... */ }

type Recipient struct {
    UserID string
}

type Sink struct{ cfg Config }

func New(cfg Config) (*Sink, error) {
    if cfg == (Config{}) {
        return nil, errors.New(errors.SinkConfig, "mysink requires a Config")
    }
    return &Sink{cfg: cfg}, nil
}

func (s *Sink) Name() string { return "mysink" }

func (s *Sink) Send(ctx context.Context, to sinks.Recipient, m sinks.Message) error {
    r, ok := to.(Recipient)
    if !ok {
        return errors.New(errors.InvalidArgument, "mysink expects mysink.Recipient")
    }
    if r.UserID == "" {
        return errors.New(errors.InvalidArgument, "mysink recipient UserID is empty")
    }
    // ...deliver...
    return nil
}
```

Two things to imitate from the reference sinks:

- Validate the config in `New`; return `errors.SinkConfig`.
- Wrap transport failures with `errors.SinkUnavailable` so the caller
  can branch on the code rather than parsing strings.

## Registration

Pass a `*sinks.Registry` into `parsec.Options` or register on the live
instance — both work.

```go
reg := sinks.NewRegistry()
reg.Register(must(mysink.New(mysink.Config{...})))

p, _ := parsec.New(parsec.Options{Sinks: reg})

// Or, post-construction:
p.Sinks().Register(must(mysink.New(mysink.Config{...})))
```

Last-write-wins: registering a different sink under the same `Name()`
silently replaces the previous one. Names should be stable string
literals, not derived at runtime.

## `PublishOrSink` semantics

The fallback rule:

```go
delivered, err := p.PublishOrSink(ctx, name, data, "mysink", to, msg)
```

1. Parse the channel name. Invalid → return the coded error.
2. Ask the broker for the presence count (number of live
   subscribers).
3. If `> 0`: publish to the broker, touch the channel, return
   `delivered=false`.
4. If `== 0`: look up the sink by name, call `Send` through the
   retry-wrapped sink. Return `delivered=true` on success, on DLQ
   landing, or the sink's error if the DLQ push itself failed.

`delivered=true` means "the sink path ran" — not "the recipient saw the
message". With phase-12 retry + DLQ wiring, a transient sink failure
retries up to `Options.SinkRetry.MaxAttempts` with exponential backoff
before being recorded in the DLQ.

## Retry contract

Every sink in the registry is wrapped in a `sinks.Retrier` at
construction time. The wrapper:

- Calls `Send` once. On success, returns `nil`.
- On a *transient* error (network blip, HTTP 5xx, 429, SMTP 4xx),
  waits `BaseBackoff * 2^(attempt-2)` (capped at `MaxBackoff`) with
  +/- `JitterFraction` jitter and retries.
- On a *terminal* error (invalid recipient, HTTP 4xx other than 429,
  config error), aborts the loop immediately.
- When the budget is exhausted, pushes the failed payload + the last
  error into the DLQ and returns `nil` to the caller. The
  failure is now recorded; the caller does not see the error.
- The DLQ push itself can fail (e.g. Redis unreachable). In that case
  the caller gets a `PARSEC_SINK_DLQ_OVERFLOW` error.

### Classification

Sinks signal whether an error is transient by wrapping it with
`sinks.Transient(err)` or `sinks.Terminal(err)`. Custom sinks should
follow the same pattern. The default classifier (`sinks.IsTransient`)
falls back to a heuristic for unwrapped errors:

- `context.DeadlineExceeded`, `context.Canceled` → terminal
- `net.OpError`, `net.Error.Timeout()`, `*url.Error` → transient
- HTTP 5xx + 429 → transient (matched on error-message prefix)
- SMTP 4xx → transient
- Everything else → terminal

## DLQ wiring

| Backend     | Activation                          |
|-------------|-------------------------------------|
| Memory DLQ  | Default when `Options.RedisClient == nil` |
| Redis DLQ   | Default when `Options.RedisClient != nil` |
| Custom DLQ  | Set `Options.DLQ` explicitly        |

A `RecipientDecoder` capability on the sink lets the Redis DLQ
round-trip the typed `Recipient` back into Go on Replay. The reference
email and webhook sinks implement it; slack does not need to because
the channel is fixed by the webhook URL.

See [ops/dlq.md](../ops/dlq.md) for the operator-side runbook.

## Manifest exposure

Registered sink names show up in the descriptor envelope under
`payload.sinks`. The CLI's `--json` flag exposes this. Clients can
probe which delivery channels are configured without reading your boot
code.

## See also

- [Library overview](overview.md).
- Built-in sinks: [email](../sinks/email.md), [slack](../sinks/slack.md),
  [webhook](../sinks/webhook.md).
