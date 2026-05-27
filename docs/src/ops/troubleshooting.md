# Troubleshooting

Common error codes and what to do about them.

```bash
# When in doubt, bump the log level and re-run the failing command.
parsec serve --addr :8000 --state-dir /var/lib/parsec
# Use the LOG_LEVEL env var (the slog default handler honors it) for
# debug-level structured output.
LOG_LEVEL=debug parsec serve --addr :8000
```

Every error that crosses a package boundary carries a `PARSEC_*` code.
The codes are stable; the messages are not. Match on the code.

## `PARSEC_AUTH_DENIED`

The mgmt bearer is missing, malformed, or signed by a key the ring no
longer trusts. Causes, in order of likelihood:

1. You forgot `--token` / `PARSEC_TOKEN`. Set it and retry.
2. The token's `kid` resolves to a retired key. Mint a fresh bearer:
   `parsec tokens mgmt`.
3. The token's `alg` or `typ` claim was tampered with. Parsec only
   accepts `HS256` / `JWT` with a `kid`.
4. The keyring was rotated and the old key retired. See
   [key rotation](key-rotation.md).

Note: a closed channel returns `PARSEC_CHANNEL_CLOSED`, NOT
`PARSEC_AUTH_DENIED`. If you see denied on a subscribe, the token is
the problem.

## `PARSEC_AUTH_EXPIRED`

The token's `exp` claim is in the past. Mint a new one. For access
tokens, use the refresh flow:

```bash
parsec tokens refresh "<refresh-token>"
```

For mgmt tokens, mint a fresh bearer under the active key:

```bash
parsec tokens mgmt --ttl 24h
```

## `PARSEC_CHANNEL_INVALID`

The name does not match the grammar enforced by
`channels.ParseName`. See [naming](../channels/naming.md). Frequent
gotchas:

- Missing `public:` / `private:` prefix.
- Uppercase or non-ASCII components.
- A `:` inside the body (the colon is reserved for the visibility
  prefix).
- Private channel without an id segment.

## `PARSEC_CHANNEL_NOT_FOUND`

The channel either:

- Was never opened.
- Was a private channel whose TTL elapsed and was reaped on the next
  sweep (default sweep interval: 30s).
- Was deleted explicitly.
- Lived in a process that has since restarted (channel state is
  in-memory).

Reopen public channels with `channels open`; private channels need to
be re-created and a fresh token pair handed out.

## `PARSEC_CHANNEL_CLOSED`

Public channel whose inactivity TTL elapsed. The record is still
there; new subscribes are refused. Reopen:

```bash
parsec channels open public:<name> --ttl 1h
```

This resets `LastActive` and emits a fresh `opened` event.

## `PARSEC_CHANNEL_EXISTS`

You called `channels create` on a private name that already exists.
Private channels do NOT support reopen — the API only goes one way.
Pick a different id segment or wait for the existing channel to
expire.

## `PARSEC_CHANNEL_TTL_EXCEEDS_MAX`

You requested a private channel with `ttl > 1h`. The cap is in
`channels.MaxPrivateTTL`. Lower the TTL and retry. If you actually
need a long-lived feed, consider whether it should be public.

## `PARSEC_BROKER_NOT_READY`

The Centrifuge node has not finished `node.Run()` yet. Almost always a
boot race — the server received an RPC before it finished initializing
the broker. Retry after a short backoff; this usually clears in under
a second.

## `PARSEC_BROKER_INTERNAL`

The OSS Centrifuge library returned an error from `Publish`,
`Presence`, or `Run`. Check the server log; the underlying message is
preserved in the wrapped error chain.

## `PARSEC_SINK_UNAVAILABLE`

A sink's `Send` failed at the transport layer. Common causes:

- SMTP server refused the relay (email sink).
- Slack webhook returned a 4xx/5xx.
- Webhook target was unreachable.

Parsec does NOT retry. Wrap the sink in your own retry layer or queue
the message upstream of `PublishOrSink`.

## `PARSEC_SINK_CONFIG_INVALID`

A sink rejected its constructor config. Re-read the sink's
documentation:

- [Email](../sinks/email.md) — requires `Addr` and `From`.
- [Slack](../sinks/slack.md) — requires `WebhookURL`.
- [Webhook](../sinks/webhook.md) — requires `URL`.

## `PARSEC_INVALID_ARGUMENT`

Generic input validation failure. The accompanying message is the
useful part — read the server log or the response body.

## `PARSEC_INTERNAL`

Unhandled, unexpected error path. File a bug with the surrounding log
context. These should be rare; treat them as actionable.

## Debug tips

- Run `parsec channels list` to confirm what the server thinks exists.
- Run `parsec channels get <name>` to inspect a single record's
  state and `last_active`.
- Use `parsec --json` (or curl `/manifest`) to verify the server
  version, configured sinks, and persistence stance.
- Watch the server log with the slog default handler on debug; key
  events (`keyring reload`, `bootstrap mgmt token`) are emitted as
  structured events.

## See also

- [Deployment](deployment.md).
- [Key rotation runbook](key-rotation.md).
