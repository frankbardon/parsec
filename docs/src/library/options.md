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
struct member, in declaration order.

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

Mtime-poll interval for the keyring file watcher. Default 5 seconds.
The watcher is what makes out-of-band `keyring.json` edits visible to
a running server. Set to 0 to disable polling — you can still trigger
a reload with `SIGHUP` or `Parsec.ReloadKeys()`.

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
| `KeyRing` | bootstrapped from `StateDir` (or ephemeral if unset) |
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
