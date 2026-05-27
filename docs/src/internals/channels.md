# Channel manager

`channels.Manager` owns the lifecycle of every channel the broker has
heard of. Its job is to track `(state, ttl, last_active)` and emit
events when a transition happens.

```go
m := channels.NewManager()
events := make(chan channels.Event, 64)
m.Subscribe(events)

go m.RunSweeper(ctx, 30*time.Second)
```

The broker side of the wire effects (kick subscribers on delete, drain
on close) is driven by these events through the manager-to-broker
bridge in `parsec.Parsec.runEventBridge`.

## State

The channel record:

```go
type Channel struct {
    Name       Name
    State      State        // open | closed | deleted
    TTL        time.Duration
    CreatedAt  time.Time
    LastActive time.Time
}
```

Three lifecycle states:

| State | Meaning |
|---|---|
| `open` | Accepting subscribes; publish path is live. |
| `closed` | Public only. Past TTL; no new subscribes but record is kept. |
| `deleted` | Removed from the manager. Subscribers must be kicked. |

The constants live in `channels/manager.go`:

```go
const MaxPrivateTTL    = 1 * time.Hour
const DefaultPublicTTL = 30 * time.Minute
```

## Transitions

```
OpenPublic        ──► state=open                 EventOpened (on new or reopen)
CreatePrivate     ──► state=open                 EventOpened
Sweep (public,    ──► state=closed               EventClosed (no kick)
       TTL hit)
Sweep (private,   ──► record removed             EventDeleted (kick)
       TTL hit)
Delete            ──► state=deleted              EventDeleted (kick)
Touch / Publish   ──► last_active = now          (no event)
```

The `state=open → state=open` re-open on `OpenPublic` for an already
open channel does NOT emit a fresh `opened` event — the contract is
"only on the closed→open edge".

## Sweep behavior

`Manager.Sweep(now time.Time) int` runs one pass over every channel:

- Skip non-open channels (closed or already-deleted records).
- Skip channels with `now.Sub(LastActive) < TTL`.
- For private channels past TTL: `delete(m.channels, key)` + emit
  `EventDeleted`.
- For public channels past TTL: flip to `StateClosed` + emit
  `EventClosed`.

The sweep is called by `RunSweeper` on a ticker, but tests usually
call `Sweep(t)` directly with a fixed clock — see
`channels/manager_test.go`.

## Event subscription

Consumers register with:

```go
events := make(chan channels.Event, 16)
m.Subscribe(events)
```

Buffer the channel. The manager does non-blocking sends — a slow
consumer drops events. Two consumers are common in a live process:

- The broker bridge (`parsec.Parsec.runEventBridge`) translates events
  into wire actions.
- Test code or telemetry hooks listen to the same stream for
  visibility.

Events are emitted from a goroutine that holds no manager lock; you do
not need to worry about re-entering the manager from a subscriber.

## Listing

`Manager.List()` returns a snapshot copy of every record. Deleted
records are excluded from `Get` but included in `List` so a tool can
see the tombstone (the state field tells you which is which).

## Thread-safety

`Manager` is safe for concurrent use. Internally it holds two
mutexes — one for the channel map, one for the subscribers slice —
plus a per-channel mutex-free copy-on-read pattern for `Get` /
`List`. The hot paths (`IsOpen`, `Touch`, `Publish`-side `Touch`) take
the read lock and the writer lock briefly, respectively.

## Test clock

`Manager.SetClock(func() time.Time)` swaps the internal time source.
Tests use this to deterministically drive TTL expiry without sleeping:

```go
m.SetClock(func() time.Time { return fixedTime })
m.Sweep(fixedTime.Add(time.Hour))
```

See `channels/manager_test.go` for the canonical pattern.

## See also

- [Public channels](../channels/public.md).
- [Private channels](../channels/private.md).
- [TTL and expiry](../channels/ttl.md).
- [Broker internals](broker.md) — how the events become wire actions.
