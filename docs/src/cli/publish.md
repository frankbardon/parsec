# `parsec publish`

Push a single message to a channel.

```bash
# Inline body
./bin/parsec publish public:webapp.system.status --data '{"msg":"hello"}'

# From a file
./bin/parsec publish public:webapp.system.status --file ./payload.json

# From stdin (pipe-friendly)
echo '{"msg":"hello"}' | ./bin/parsec publish public:webapp.system.status
```

## What it does

`publish` calls the `Publish` RPC. The server validates the channel
name through `channels.ParseName`, confirms the channel exists and is
open, ships the data to Centrifuge, and touches `LastActive` so the
sweeper does not close the channel out from under an active publisher.

Bodies are opaque bytes. Parsec does not impose a schema; the contract
is between the publisher and the subscriber. JSON is conventional
because every consumer language can parse it cheaply, but you can ship
protobuf, msgpack, or raw text just as easily.

## Source precedence

Exactly one source is honored, picked in this order:

1. `--data` / `-d` — inline.
2. `--file` / `-f` — read the named file.
3. stdin — only when neither of the above is set.

Mixing sources is an operator error and the CLI returns
`PARSEC_INVALID_ARGUMENT`.

## What you get back

The CLI prints a descriptor envelope with the broker's publish ack:

```json
{
  "code": "OK",
  "payload": {
    "offset": 14,
    "epoch": "abc123"
  }
}
```

The offset is the per-channel monotonic publication index Centrifuge
assigns. Late subscribers use `(offset, epoch)` to ask for a recovery
stream — they catch up on the publications they missed.

## Failure modes

| Code | Cause |
|---|---|
| `PARSEC_CHANNEL_INVALID` | Name does not match the grammar. |
| `PARSEC_CHANNEL_NOT_FOUND` | Channel was never opened, or expired. |
| `PARSEC_CHANNEL_CLOSED` | Public channel past its TTL — reopen first. |
| `PARSEC_AUTH_DENIED` | Missing or expired bearer. |
| `PARSEC_BROKER_NOT_READY` | Server is still booting; retry shortly. |

For private channels, the bearer is the operator mgmt token — not the
end-user access token. End users publish via the websocket.

## Reference

```text
NAME:
   parsec publish - Publish a message to a channel

USAGE:
   parsec publish [options] <channel-name>

OPTIONS:
   --server string           (default: "http://localhost:8000") [$PARSEC_SERVER]
   --token string             [$PARSEC_TOKEN]
   --data string, -d string  Inline message body
   --file string, -f string  Read message body from file
   --help, -h                show help

GLOBAL OPTIONS:
   --json  Output the parsec manifest as a descriptor envelope
```

## See also

- [channels](channels.md) — open/create before publishing.
- [Library: PublishOrSink](../library/overview.md) — fallback to a sink
  when no subscriber is listening.
