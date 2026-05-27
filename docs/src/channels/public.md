# Public channels

Public channels are broadcast feeds: anyone can subscribe, no token
required.

```bash
./bin/parsec channels open public:webapp.system.status
echo '{"msg":"deploy started"}' | ./bin/parsec publish public:webapp.system.status
./bin/parsec subscribe public:webapp.system.status
```

The visibility prefix `public:` is what unlocks the no-auth subscribe
path. The grammar is documented in [naming](naming.md); anything not
matching `public:<app>.<domain>[.<id>][.<topic>]` is rejected by
`channels.ParseName` before the manager ever sees it.

## Lifecycle

Public channels move between three states:

| State | Meaning | New subscribers | Existing subscribers |
|---|---|---|---|
| `open` | Active | Accepted | Receive publications |
| `closed` | Inactivity TTL exceeded | Rejected | Drain naturally (not kicked) |
| `deleted` | Removed by an operator | Rejected | Unsubscribed |

The transition from `open` to `closed` happens during the manager's
periodic sweep — every 30 seconds by default. A closed channel can be
reopened with another `channels open` call, which resets `last_active`
and emits a fresh `opened` event.

## TTL semantics

Public channels have an inactivity TTL: any `Publish` (or explicit
`Touch`) resets the clock. The default is 30 minutes; override with
`--ttl 2h` on `channels open`. Two important properties:

- Closed public channels are not removed. The record stays in the
  manager so an operator can reopen it without losing the name.
- Closed public channels do not kick subscribers. They simply refuse
  new ones. This matches the "fade out" intent of a public feed — the
  broadcaster has stopped, but the people already listening can finish
  what they're hearing.

See [TTL and expiry](ttl.md) for the full lifecycle diagram and how it
differs from private.

## History

Centrifuge keeps a per-channel history buffer so a late subscriber can
catch up. The defaults: 100 publications, 5-minute retention. These are
broker-side knobs configured at boot via `broker.Options`:

- `HistorySize` — bound on retained publications per channel
- `PublicHistoryTTL` — how long publications stay in history

Both default to safe single-node values. Embedders who need different
windows set them through `parsec.Options.BrokerOptions`.

## Delete

Public channels do not auto-delete (only private does). You explicitly
remove one with:

```bash
./bin/parsec channels delete public:webapp.system.status
```

Delete is final. The manager flips the record to `deleted`, the broker
unsubscribes every connected client, and the name becomes unusable —
even reopening it returns `PARSEC_CHANNEL_NOT_FOUND`. Create a new
channel under a different name (or wait for the manager state to be
wiped on restart).

## Typical use

- System status feeds: `public:<app>.system.status`
- Org-wide counters: `public:scoreboard.counter.<metric>`
- Maintenance announcements: `public:<app>.broadcast.maintenance`

Anything that should reach an authenticated population belongs in a
[private channel](private.md) instead.
