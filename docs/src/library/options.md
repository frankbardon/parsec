# `parsec.New` and `Options`

`parsec.New(Options{})` is the only constructor. Every field on
`Options` is optional and falls back to a documented default.

```go
p, err := parsec.New(parsec.Options{
    StateDir:           "/var/lib/parsec",
    AccessTokenTTL:     5 * time.Minute,
    MaxRefreshTokenTTL: time.Hour,
    SweepInterval:      30 * time.Second,
})
```

The full struct is in `parsec.go`. Each field below corresponds to one
struct member, in declaration order. Fields not documented here are
either niche or self-describing — `parsec.go` is the authority.

## `FS afero.Fs`

The filesystem used by anything in the library that touches disk —
chiefly the keyring loader. Defaults to `afero.NewOsFs()`. Override in
tests with `afero.NewMemMapFs()` to keep the keyring fully in memory.
Library code never calls `os.Open` directly; every disk access goes
through this handle.

## `Sinks *sinks.Registry`

The out-of-band delivery registry. Defaults to an empty registry
(`sinks.NewRegistry()`); register sinks before calling `Run` if you
intend to use `PublishOrSink`. See [custom sinks](sinks.md).

## `KeyRing *auth.KeyRing`

The HMAC signing ring. If you provide one, `parsec.New` uses it as-is
and ignores `StateDir`. Most embedders leave this nil and pass
`StateDir` instead — fewer ways to get rotation wrong.

## `StateDir string`

When set, makes the keyring file-backed at
`<StateDir>/keyring.json` (mode `0600`, parent `0700`). Bootstrap is
automatic: if the file doesn't exist, the library generates an initial
active key and writes it. Without this option AND without `KeyRing`,
the library logs a loud warning and uses an ephemeral ring — tokens do
not survive a restart.

## `KeyringPollInterval time.Duration`

How stale this node's view of the keyring may get. Default 5 seconds.

With `StateDir` it is the mtime-poll interval, which makes out-of-band
`keyring.json` edits visible to a running server. With Redis it is the
interval between reconcile reads, which is what catches a rotation whose
pub/sub event this node never received — Redis pub/sub is at-most-once, so
without the reconcile a node disconnected at publish time would serve a
stale ring indefinitely.

A negative value disables the poll; you can still trigger a reload with
`SIGHUP` or `Parsec.ReloadKeys()`.

## `RedisClient redis.UniversalClient`

Enables multi-node mode. When set, the channel registry, keyring, DLQ,
rate limiter, cache and refresh store all share this client. Nil means
single-node in-memory.

Pass a client directly when you need something `RedisAddr` cannot express:
sentinel or cluster topologies, a custom dialer, or connection pool
tuning.

## `RedisAddr string`

Convenience alternative to `RedisClient`: parsec builds the client. Accepts

```text
host:port
redis://[[user][:password]@]host:port[/db][?opt=val]
rediss://...   (TLS)
tcp://...      (alias for redis://)
unix:///path/to/socket
```

Sentinel and cluster URLs are rejected — the shared client cannot be built
from one, so supply `RedisClient` instead. A malformed address fails
`parsec.New` rather than surfacing later as a dial error.

`parsec.New` also pings Redis before building anything on it, so an
unreachable Redis fails the boot. `RedisPingTimeout` bounds that check
(default 5s); a negative value skips it, though note the centrifuge broker
shard built from this address connects eagerly regardless, so tolerating an
absent Redis at boot also needs an explicit `RedisShards`.

## `RedisAuth RedisAuth`

Credentials and TLS for the client built from `RedisAddr`, as
`{Username, Password, DB *int, TLSConfig *tls.Config}`. Set fields override
whatever the address encoded, so a password from a secret file wins over
one in the URL. A nil `DB` leaves the address's database alone; a pointer
to `0` forces database 0. Unused when `RedisClient` is supplied directly.

## Keyring precedence

The four options above interact with `KeyRing` and `StateDir` in one fixed
order:

| Precedence | Condition | Store |
|---|---|---|
| 1 | `KeyRing` non-nil | None — you own persistence |
| 2 | Redis configured | `auth.RedisKeyRingStore`, shared across nodes |
| 3 | `StateDir` set | `auth.FileKeyRingStore` at `<StateDir>/keyring.json` |
| 4 | neither | Ephemeral; tokens die with the process |

Redis beating `StateDir` is the part that surprises people: with both set,
`keyring.json` is never read or written and Redis holds the only copy of
the signing keys. `parsec.New` logs a warning when it sees both. Redis
then needs AOF on and eviction off — see
[Redis durability](../ops/deployment.md#redis-durability).

## `RedisKeyPrefix string`

Namespaces every parsec key in Redis. Default `parsec`. Override when
multiple deployments share an instance — it separates the keyring, channel
registry, DLQ and rate-limit keyspaces in one go.

## `NodeID string`

Identifies this process across the cluster, used to dedupe cross-node
pub/sub events. Empty auto-generates one.

## `AccessTokenTTL time.Duration`

Overrides the default access-token lifetime (5 minutes). The issuer
clamps to `[1m, 1h]`, so values outside that range are rejected at
issuance time. Shorter access TTLs mean more refreshes — pick based on
how aggressively you need revocation.

## `MaxRefreshTokenTTL time.Duration`

Overrides the default refresh-token cap (1 hour). The actual refresh
TTL is `min(channel.TTL, MaxRefreshTokenTTL)` — you cannot mint a
refresh token that outlives its channel.

## `BrokerOptions broker.Options`

Forwarded verbatim to `broker.New`. The fields that matter most:

- `HistorySize` — per-channel history bound (default 100).
- `PublicHistoryTTL` / `PrivateHistoryTTL` — history retention windows
  (both default 5m).
- `LogHandler` — Centrifuge-level log sink.
- `SubscribeAuthorizer` — `parsec.New` REPLACES whatever you put here
  with one that combines `Manager.IsOpen` and the token-based
  authorizer. Set this on `broker.Options` only if you are wiring the
  broker without `parsec.New`.

## `SweepInterval time.Duration`

How often the channel manager runs its expiry sweep. Default 30
seconds. Shorter intervals make TTL enforcement more responsive at the
cost of cycles; longer intervals delay both `closed` (public) and
`deleted` (private) transitions.

## `Logger *slog.Logger`

Receives boot warnings, keyring reload events, and other operational
messages. Defaults to `slog.Default()`. Inject your own logger if you
want a structured output stream — every message carries field
attributes (`active_key_id`, `path`, etc.) suitable for JSON sinks.

## Default summary

| Field | Default |
|---|---|
| `FS` | `afero.NewOsFs()` |
| `Sinks` | empty `sinks.Registry` |
| `KeyRing` | bootstrapped from Redis if configured, else `StateDir`, else ephemeral |
| `RedisClient` / `RedisAddr` | unset — single-node in-memory |
| `RedisKeyPrefix` | `parsec` |
| `RedisPingTimeout` | 5s |
| `StateDir` | "" (ephemeral keyring) |
| `KeyringPollInterval` | 5s |
| `AccessTokenTTL` | 5m |
| `MaxRefreshTokenTTL` | 1h |
| `BrokerOptions.HistorySize` | 100 |
| `BrokerOptions.{Public,Private}HistoryTTL` | 5m |
| `SweepInterval` | 30s |
| `Logger` | `slog.Default()` |

## See also

- [Go API overview](overview.md) — putting it all together.
- [Custom sinks](sinks.md) — populate the registry.
- [Key rotation](../ops/key-rotation.md) — what `StateDir` actually
  buys you.
