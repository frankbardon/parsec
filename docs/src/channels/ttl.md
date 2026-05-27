# TTL and expiry

Every channel carries an inactivity TTL; the manager's sweep loop is
what enforces it.

```bash
# Open with an explicit TTL
./bin/parsec channels open public:webapp.system.status --ttl 1h

# Inspect TTL + last_active
./bin/parsec channels get public:webapp.system.status
```

## States

Three states, one transition diagram:

```
                 +-------+
                 | open  |
                 +-------+
                /         \
   sweep (public,         sweep (private,
    TTL exceeded)          TTL exceeded)
              |             |
              v             v
         +--------+    +---------+
         | closed |    | deleted |
         +--------+    +---------+
              \          ^
        reopen \        / explicit Delete
                v      /
             +-------+
             | open  |  (closed -> open via OpenPublic)
             +-------+
```

The constants live in `channels/manager.go`:

```go
const MaxPrivateTTL    = 1 * time.Hour
const DefaultPublicTTL = 30 * time.Minute
```

Default private TTL is 15 minutes — see `CreatePrivate` in the same
file.

## Sweep behavior

`Manager.Sweep(now)` runs once per `SweepInterval` (default 30s).
For every channel in `StateOpen`:

- If `now.Sub(LastActive) < TTL` — skip.
- Else, if the name is `public:` — flip to `closed`, emit `EventClosed`.
  Existing subscribers stay attached and continue to receive
  publications until they disconnect on their own.
- Else (`private:`) — remove the record entirely, emit `EventDeleted`.
  The broker bridge kicks every connected subscriber.

That asymmetry is intentional. Public channels are "broadcasts" — the
broadcaster fading out should not look the same as a private
per-tenant feed silently disappearing.

## Touch

A channel resets its `LastActive` on three occasions:

- `OpenPublic` on a closed channel — reopens and resets.
- `Publish` — every publish path calls `Touch` on success.
- An explicit `Manager.Touch` call from library code.

There is no CLI `touch` subcommand because operator workflows do not
need one. If you want to keep a channel alive without sending data,
either republish heartbeat messages or extend the TTL by calling
`channels open` again with a larger `--ttl`.

## Public vs private at a glance

| Behavior | Public | Private |
|---|---|---|
| Default TTL | 30m | 15m |
| Max TTL | unbounded | 1h |
| TTL exceeded → | `closed` (record kept) | `deleted` (record reaped) |
| Subscribers when expired | drain naturally | kicked |
| Reopenable | yes (`channels open` again) | no — create fresh |
| Auto-delete | never | on TTL |

See [public channels](public.md) and [private channels](private.md) for
the surface-by-surface details, and the
[architecture overview](../internals/architecture.md) for how the
manager-event bridge translates sweep events into broker actions.

## Events

The manager emits typed events on every transition:

| Kind | When | Bridge effect |
|---|---|---|
| `opened` | Public re-open or private create | New subscribers accepted |
| `closed` | Public TTL exceeded | No new arrivals; existing drain |
| `deleted` | Explicit delete or private TTL exceeded | Broker unsubscribes everyone |

Subscribers register via `Manager.Subscribe(chan<- Event)`; the manager
sends non-blocking, so subscribers should buffer. See
[broker internals](../internals/broker.md) for the wire side.
