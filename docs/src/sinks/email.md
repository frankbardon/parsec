# Email sink

A thin SMTP relay. Delivers the `Message` via `net/smtp.SendMail`.

```go
import (
    "github.com/frankbardon/parsec"
    "github.com/frankbardon/parsec/sinks/email"
)

es, _ := email.New(email.Config{
    Addr:     "smtp.example.com:587",
    From:     "parsec@example.com",
    Username: "parsec",
    Password: os.Getenv("SMTP_PASSWORD"),
})

p, _ := parsec.New(parsec.Options{})
p.Sinks().Register(es)
```

Once registered, you reference it by name in `PublishOrSink`:

```go
delivered, err := p.PublishOrSink(ctx,
    "private:webapp.user.42.downloads",
    []byte(`{"status":"ready"}`),
    "email",
    email.Recipient{Address: "user@example.com"},
    parsec.Message{
        Subject: "Your download is ready",
        Body:    "Tap the link in the app.",
    })
```

## Configuration

`email.Config`:

| Field | Required | Notes |
|---|---|---|
| `Addr` | yes | SMTP host:port — e.g. `smtp.example.com:587`. |
| `From` | yes | RFC 5322 mailbox-list, used as the envelope sender. |
| `Username` | no | Omit for relays without auth (e.g. local Postfix). |
| `Password` | no | Required when `Username` is set. |

If `Username` is set the sink uses `smtp.PlainAuth` over the
connection. If not, the sink connects unauthenticated. The host
component is parsed from `Addr` and used as the `PlainAuth` host
identity.

## Recipient

`email.Recipient`:

```go
type Recipient struct {
    Address string
}
```

`Address` is required and must be an RFC 5322 mailbox. Empty addresses
return `PARSEC_INVALID_ARGUMENT`.

## Body shape

The sink stitches together a minimal RFC 5322 message:

```
From: <cfg.From>
To: <recipient.Address>
Subject: <message.Subject>

<message.Body>
```

Nothing else. No HTML wrapping, no MIME alternative, no templating —
the publisher is responsible for whatever content shape the recipient
expects. If you need richer email (HTML bodies, attachments, custom
headers from `Metadata`), wrap this sink with your own and pre-format
the body before calling `Send`.

## Failure semantics

| Condition | Returned code |
|---|---|
| Missing `Addr` or `From` at construction | `PARSEC_SINK_CONFIG_INVALID` |
| Wrong recipient type | `PARSEC_INVALID_ARGUMENT` |
| Empty recipient address | `PARSEC_INVALID_ARGUMENT` |
| `smtp.SendMail` returns error | `PARSEC_SINK_UNAVAILABLE` |

The sink does NOT retry. Network blips return immediately. If you need
durability, wrap the sink in your own retry layer or queue the message
before calling `PublishOrSink`.

## See also

- [Custom sinks](../library/sinks.md) — write your own.
- [Slack sink](slack.md) and [Webhook sink](webhook.md) — the other
  reference implementations.
