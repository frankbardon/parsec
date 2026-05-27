# Broker (Centrifuge)

`broker.Broker` is a thin wrapper around `*centrifuge.Node`. Its job is
to keep Parsec's other packages from depending directly on the
Centrifuge OSS library and to install the two hooks Parsec actually
cares about: connect and subscribe authorization.

```go
br, err := broker.New(broker.Options{
    HistorySize:         100,
    PublicHistoryTTL:    5 * time.Minute,
    PrivateHistoryTTL:   5 * time.Minute,
    SubscribeAuthorizer: myAuthorizer, // optional
})
go br.Run(ctx)
```

## What gets wrapped

`*centrifuge.Node` is the underlying broker. Parsec exposes a narrow
surface on top:

| Method | Centrifuge equivalent | Notes |
|---|---|---|
| `New(opts)` | `centrifuge.New(cfg)` | Installs the connect + subscribe hooks. |
| `Run(ctx)` | `node.Run()` + `node.Shutdown` | Blocks until ctx is done; 5s shutdown grace. |
| `Publish(ctx, ch, data)` | `node.Publish(...)` | History size + TTL filled in from Options. |
| `Presence(ctx, ch)` | `node.Presence(...)` | Returns just the subscriber count. |
| `UnsubscribeAll(ch)` | iterate `hub.Connections()` | Used by the bridge on `EventDeleted`. |
| `Node()` | n/a | Escape hatch — the HTTP mount needs it to install the websocket handler. |

## Connect hook

`OnConnecting` is installed at construction time with a permissive
default:

```go
node.OnConnecting(func(ctx context.Context, _ centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
    return centrifuge.ConnectReply{Credentials: &centrifuge.Credentials{UserID: ""}}, nil
})
```

Anonymous connect is intentional. The real authorization happens at
`SUBSCRIBE` time, because that is when the channel name is known. An
attacker who connects but never subscribes does nothing useful.

Embedders that need per-connection identity (e.g. browser SSO) can
override this hook by calling `broker.Node().OnConnecting(...)` after
`broker.New`.

## Subscribe hook

The subscribe path is the security boundary. For every subscribe
attempt:

1. `channels.ParseName(event.Channel)` — invalid grammar → reject.
2. `SubscribeAuthorizer(ctx, userID, name, event)` — runs the
   configured authorizer.

`parsec.New` replaces whatever `SubscribeAuthorizer` you put on
`broker.Options` with one that does TWO checks:

```go
if !manager.IsOpen(ch) {
    return errors.New(errors.ChannelClosed, "channel not open")
}
return tokenAuth(ctx, userID, ch, event)
```

So a closed channel rejects new subscribes BEFORE the token is
even examined. The token check (`auth.NewSubscribeAuthorizer(verifier)`)
allows any well-formed public channel and validates the embedded
access token against private channels.

The default authorizer (when you skip `parsec.New`) is stricter still
— it rejects every private channel outright:

```go
func defaultSubscribeAuthorizer(_ context.Context, _ string, ch channels.Name, _ centrifuge.SubscribeEvent) error {
    if ch.IsPrivate() {
        return errors.New(errors.AuthDenied, "private channel requires a SubscribeAuthorizer")
    }
    return nil
}
```

Use that when you wire `broker.New` directly and have not yet built a
token authorizer.

## Publish path

`Publish` is a thin layer over `node.Publish` with history options
chosen by visibility:

```go
node.Publish(ch.String(), data,
    centrifuge.WithHistory(opts.HistorySize, b.historyTTLFor(ch)))
```

Public and private channels can be tuned independently via
`PublicHistoryTTL` and `PrivateHistoryTTL`. Defaults are 5 minutes
each.

## When the broker is "not ready"

`Publish` checks an internal `started` flag and returns
`PARSEC_BROKER_NOT_READY` if `Run` has not yet executed
`node.Run()`. Callers should retry on this code — it usually means the
server is still booting.

## Deeper Centrifuge internals

Parsec relies on Centrifuge for the actual transport layer (websocket,
SSE, hub fanout, history). The OSS library's design is well-documented
upstream:

- The Centrifuge documentation:
  `https://centrifugal.dev/docs/server/library_intro`
- The Go API reference: `https://pkg.go.dev/github.com/centrifugal/centrifuge`

Parsec deliberately does NOT re-document those internals. The contract
is "anything the OSS library can do is reachable via `Broker.Node()`";
the wrapper exists to keep the rest of Parsec from coupling to
Centrifuge symbols directly.

## See also

- [Architecture](architecture.md).
- [Channel manager](channels.md).
- [Channel naming](../channels/naming.md) — the parse step.
