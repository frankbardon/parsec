# Go API overview

Parsec's primary deliverable is a Go library. The CLI and Twirp
surfaces are translators on top of the same `*parsec.Parsec` value.

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/frankbardon/parsec"
)

func main() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    p, err := parsec.New(parsec.Options{StateDir: "/var/lib/parsec"})
    if err != nil { log.Fatal(err) }
    go func() { _ = p.Run(ctx) }()

    ch, _ := p.OpenPublic("public:webapp.system.status", time.Hour)
    _, _ = p.Publish(ctx, ch.Name.String(), []byte(`{"msg":"hello"}`))
}
```

The package import path is `github.com/frankbardon/parsec`. Everything
else (`auth`, `broker`, `channels`, `sinks`) lives one directory down.

## Construction

`parsec.New(opts)` builds the instance but does not start anything. It
assembles the keyring, the signer/verifier/issuer triad, the channel
manager, and the broker — and wires them together so a closed channel
rejects subscribes before the bridge runs. Every field on `Options` has
a default; see [parsec.New & Options](options.md) for the per-field
walkthrough.

## Lifecycle

The instance is inert until you call `Run(ctx)`:

```go
go func() {
    if err := p.Run(ctx); err != nil {
        log.Printf("parsec exited: %v", err)
    }
}()
```

`Run` launches three goroutines and then blocks on the Centrifuge node
inside the same call:

- The sweep loop (`Manager.RunSweeper`) that ticks every
  `SweepInterval`.
- The manager-to-broker event bridge that translates lifecycle events
  into broker actions (close = no kick; deleted = kick everyone).
- The keyring file watcher (when `StateDir` is set) that hot-reloads
  the ring on mtime changes.

Cancel the context to shut everything down. The broker drains over a
5-second grace window before `Run` returns.

## Surfaces and the embedded model

`parsec.Parsec` exposes the building blocks for an embedder to mount
its own surfaces:

| Accessor | Returns |
|---|---|
| `p.Broker()` | The Centrifuge wrapper — mount `/connection/websocket` from this. |
| `p.Manager()` | The channel lifecycle manager — subscribe to events. |
| `p.Sinks()` | The sink registry — register custom sinks. |
| `p.Verifier()` | Token verifier — use for custom subscribe auth. |
| `p.Issuer()` | Token issuer — mint access / refresh / mgmt tokens. |
| `p.KeyRing()` | The HMAC ring — inspect, never mutate directly. |

The standard server in `internal/server/` shows how the CLI's `serve`
wires Twirp, websocket, SSE, and healthz onto these accessors. You can
reproduce that wiring inside your own HTTP mux, or you can call
`parsec.Parsec` methods directly from a non-HTTP context (e.g. a
worker that just publishes).

## Publishing

Two flavors:

```go
// Always publishes; if nobody is listening, the message is buffered in
// Centrifuge's history but no out-of-band delivery happens.
res, err := p.Publish(ctx, "private:webapp.user.42.downloads",
    []byte(`{"status":"ready"}`))

// If presence == 0, falls back to the named sink. If presence > 0,
// publishes normally and returns delivered=false (since no sink ran).
delivered, err := p.PublishOrSink(ctx,
    "private:webapp.user.42.downloads",
    []byte(`{"status":"ready"}`),
    "email",
    email.Recipient{Address: "user@example.com"},
    parsec.Message{Subject: "Your download is ready", Body: "..."})
```

See [custom sinks](sinks.md) for writing your own.

## Private channels

`p.CreatePrivate(subject, name, ttl)` mints a record AND issues the
token pair in one call:

```go
creds, err := p.CreatePrivate("user-42",
    "private:webapp.user.42.downloads",
    30 * time.Minute)
// creds.AccessToken, creds.RefreshToken, expiries
```

The library is the only place the tokens exist after this call. Don't
log them.

## Where to read next

- [parsec.New & Options](options.md) — every option, default, and
  intent.
- [Custom sinks](sinks.md) — the `Sink` interface and registry.
- [Architecture overview](../internals/architecture.md) — how the
  pieces talk to each other.
