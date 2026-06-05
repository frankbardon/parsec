# Schema Registry Broadcast

The schema registry exposes two surfaces: an HTTP endpoint at
`/parsec/schemas` for snapshot fetches, and a parsec channel onto which
every registry mutation is published in real time. Subscribers join
the channel and hot-reload definitions as patterns are registered,
updated, or deregistered — no polling.

This page covers the broadcast surface. The HTTP handler is documented
in [Architecture Overview](../internals/architecture.md).

## Channel

The default broadcast channel is:

```
public:parsec.registry.schemas
```

Exposed as `schema.ChangeChannel` in the library. The channel is
public, so any client carrying a subscribe authorizer that allows
public channels can listen — typically every client.

Operators that want a different channel name pass it via
`schema.PublisherOptions.Channel`. The wire shape is unchanged.

## Wire shape

Each publication is a JSON-encoded `schema.Change`:

```json
{
  "kind": "registered",
  "pattern": "sessions:{id}",
  "schema": {
    "pattern": "sessions:{id}",
    "version": 1,
    "aspects": { "data": { "name": "data", "payload_schema": { "type": "object" } } }
  },
  "at": "2026-06-05T20:14:30.123Z"
}
```

`kind` is one of `registered`, `updated`, or `deregistered`. The
`schema` field is omitted on `deregistered` (the pattern is gone — the
client deletes the cache entry).

## Wiring

The `schema.Publisher` bridges any `schema.Registry` to any
`schema.PublishFunc`. Wire it once at boot, alongside the HTTP
handler:

```go
reg := schema.NewMemoryRegistry()

pc, _ := parsec.New(parsec.Options{
    SchemaHandler: schema.Handler(reg),
    // ...
})

pub := schema.NewPublisher(reg, func(ctx context.Context, ch string, data []byte) error {
    _, err := pc.Publish(ctx, ch, data)
    return err
}, schema.PublisherOptions{
    EnsureChannel: func(ch string) error {
        _, err := pc.OpenPublic(ch, time.Hour)
        return err
    },
})

go func() { _ = pub.Run(ctx) }()
```

`EnsureChannel` is invoked exactly once at `Run` start. Use it to open
the broadcast channel on the broker before the first publication —
parsec rejects publishes to channels that have not been opened.

The Publisher is best-effort: marshal or publish errors are logged at
WARN and the loop continues. Subscribers that need exactly-once
semantics re-sync against the HTTP snapshot whenever they detect a
gap.

## Client behavior

A client library following the recommended pattern:

1. Fetches `GET /parsec/schemas` on startup, populates a local cache.
2. Subscribes to `public:parsec.registry.schemas`.
3. For each `Change` envelope:
   - `registered` / `updated` — replace the cache entry.
   - `deregistered` — drop the cache entry; pending publications to
     the removed pattern fail-closed at the validation layer.
4. On a transport reconnect, re-fetch the snapshot. The broadcast is
   not durable across long disconnects.

## Cardinality

The broadcast channel is a single channel name, shared by every
subscriber. The publisher fans out via the normal Centrifuge broker
path — no per-subscriber state in the registry itself.
