# Deployment

How to run `parsec serve` in something resembling production. The
single-node story is the default; the multi-node story attaches a Redis
backend.

```bash
parsec serve \
  --addr :8000 \
  --state-dir /var/lib/parsec \
  --keyring-poll 5s
```

## Topology: single node

The simplest deployment is one Parsec process with an in-memory channel
registry and a file-backed keyring. Centrifuge holds tens of thousands
of concurrent websockets per core — single-node carries a lot of load.

## Topology: clustered

Point `--redis-addr`, the YAML `redis.addr` field, or
`Options.RedisClient` at a shared Redis and the broker, channel registry,
keyring, DLQ, and rate limiter all switch to their Redis-backed
implementations. Multiple Parsec nodes then share the same truth about
open channels, keys, and sink failures. See
[key rotation](key-rotation.md#multi-node-deployments) for the rotation
walkthrough.

Address forms: `host:port`, or a `redis://`, `rediss://`, `tcp://` or
`unix://` URL. Credentials can ride in the URL
(`redis://user:pass@host:6379/0`) or come from the `redis.username` /
`redis.password` / `redis.db` fields, which win over the URL so a secret
can be injected from the environment. `rediss://` implies TLS with system
roots; use the `redis.tls` section for a private CA. Sentinel and cluster
URLs are rejected — build the client yourself and pass
`Options.RedisClient`.

A malformed address fails at startup rather than surfacing later as a
dial error, and Parsec pings Redis before building anything on top of it,
so an unreachable Redis also fails the boot.

## Required state

| Path | What | Survives restart? |
|---|---|---|
| `<state-dir>/keyring.json` | HMAC signing keys, single-node only | Yes |
| `<prefix>:keyring` in Redis | HMAC signing keys when Redis is configured | Only if Redis persists — see below |
| `projects/<p>/secrets/<id>` in Google Secret Manager | Signing keys when `Options.KeyRingStore` is the [Secret Manager store](gcp-secret-manager.md) | Yes |
| In-memory channel map | Open channels, private records | No |
| In-memory subscriber set | Live websocket connections | No |

## Redis durability

**When Redis is configured it holds the keyring, and it is the only
copy.** Redis wins the keyring precedence, so `--state-dir` is ignored
for key storage — Parsec logs a warning if you set both, because a
`state_dir` in the config looks like a backup and is not one.

An empty keyspace is indistinguishable from a first boot: Parsec
bootstraps a brand-new ring, and every token in the fleet — including the
bootstrap mgmt token in your secret manager — stops verifying. Two
settings prevent that:

```bash
redis-server --appendonly yes --maxmemory-policy noeviction
```

| Setting | Why |
|---|---|
| `appendonly yes` | Without AOF, a restart loses writes back to the last RDB snapshot. Default save points can be an hour stale, and a keyring write is a single key — exactly the change a snapshot interval misses. |
| `maxmemory-policy noeviction` | The keyring key carries no TTL, but an `allkeys-*` policy evicts it anyway once `maxmemory` is reached. A `volatile-*` policy is also safe, since the key is never given an expiry. |

Parsec checks both at boot and warns if they look wrong. The check is
advisory: managed providers often block `CONFIG GET`, and an unreadable
setting is skipped rather than guessed at, so verify it yourself on a
managed instance.

Back the ring up out of band before you need it:

```bash
parsec keys export --redis-addr <addr> > keyring-backup.json   # 0600 it
parsec keys import --redis-addr <addr> < keyring-backup.json    # restore
```

The manifest reports `"persistence": "in-memory"` so clients can
discover the stance without reading docs. The contract: a restart wipes
every channel, every subscriber must reconnect, every private channel
must be re-created with fresh tokens.

If that is a problem for your use case, Parsec is the wrong primitive
— use a real durable queue.

## Keyring stores

The ring is resolved in this order, and the first one set wins:

| Precedence | Configured by | Shared across nodes? |
|---|---|---|
| 1 | `Options.KeyRing` (a ring you built yourself) | You own it |
| 2 | `Options.KeyRingStore` (e.g. [Google Secret Manager](gcp-secret-manager.md)) | Yes |
| 3 | `RedisClient` / `redis.addr` | Yes |
| 4 | `--state-dir` → `<state-dir>/keyring.json` | No |
| 5 | nothing — an ephemeral ring, logged loudly | No |

`Options.KeyRingStore` is a library-only option: it takes a constructed
store, which the CLI has no way to spell in a flag. `parsec serve` from
this repo therefore reaches levels 3–5 only. An embedder wiring their own
`main` gets the rest — see
[Google Secret Manager](gcp-secret-manager.md) for the one that ships.

Whichever store wins, it holds the **only** copy of the signing keys.
Anything lower in the table is ignored for key storage, and Parsec warns
at boot when you set two of them, because the ignored one looks like a
backup and is not one.

Every shared store reports its revision on `parsec_keyring_version`.
Nodes agreeing on the ring report the same number; a node stuck on an old
number is serving keys the rest of the fleet has moved past.

## `--state-dir`

Always pass it in production **unless Redis is configured**, in which
case Redis holds the keyring and this flag does nothing for key storage.
Without either, the keyring is
ephemeral and every restart mints a new bootstrap token under a brand
new active key. Existing browser clients can no longer refresh and
must re-authenticate. With `--state-dir`, the ring survives the
restart and active sessions tolerate a quick bounce.

The directory is created with mode `0700`; the keyring file with mode
`0600`. Both are owned by the running uid. Mount your state volume so
the uid is consistent across restarts.

## Environment variables

The CLI reads these at boot. The library does not — it takes typed
values via `Options`.

| Variable | Default | Notes |
|---|---|---|
| `PARSEC_STATE_DIR` | "" | Same as `--state-dir`. |
| `PARSEC_MGMT_SUBJECT` | `operator` | Subject claim on the bootstrap mgmt token. |
| `PARSEC_MGMT_TTL` | `24h` | Bootstrap mgmt TTL. |
| `PARSEC_KEYRING_POLL` | `5s` | How stale this node's keyring view may get: mtime-poll interval, or the Redis reconcile interval. Negative disables it. |
| `PARSEC_REDIS_ADDR` | "" | Same as `--redis-addr`. |
| `PARSEC_REDIS_KEY_PREFIX` | `parsec` | Same as `--redis-key-prefix`. |
| `PARSEC_NO_AUTH` | unset | Dangerous; disables the bearer middleware. Dev only. |
| `PARSEC_SERVER` | `http://localhost:8000` | Default `--server` for the client subcommands. |
| `PARSEC_TOKEN` | "" | Mgmt bearer for the client subcommands. |

## Capturing the bootstrap token

`parsec serve` prints the bootstrap mgmt token to STDERR exactly once.
A typical systemd unit captures it like this:

```ini
[Service]
ExecStart=/usr/local/bin/parsec serve --addr :8000 --state-dir /var/lib/parsec
StandardError=append:/var/log/parsec/boot.log
```

Then grep the log:

```bash
grep "bootstrap mgmt token" /var/log/parsec/boot.log
```

If you persist the keyring, the same token continues to work across
restarts — fetch it once, store it in your secret manager, move on.

## Health and observability

| Endpoint | Purpose |
|---|---|
| `/healthz` | Liveness probe. Returns 200 from the moment the HTTP listener is up — it does **not** wait for `node.Run` or check Redis, so treat it as "the process is alive", not "the node is ready to serve". |
| `/manifest` | Descriptor envelope — surfaces, sinks, version. |
| `/metrics` | Prometheus exposition, including the `parsec_*` collectors. See [observability](observability.md). |

Dependency failures surface at boot instead: an unreachable or
misconfigured Redis fails `parsec serve` outright rather than leaving a
process that answers `/healthz` while nothing works.

## Upgrades

A restart kills channel state. The supported upgrade procedure:

1. Drain or accept the disruption (browsers reconnect; private
   channels need fresh tokens).
2. `systemctl restart parsec` (or your orchestrator's equivalent).
3. The new process loads the same `keyring.json`; active operator
   bearers continue to verify.

If you cannot tolerate the disconnect window, queue a maintenance
banner on `public:<app>.broadcast.maintenance` and run during a
quiet period.

## See also

- [Key rotation runbook](key-rotation.md).
- [Troubleshooting](troubleshooting.md).
- [serve](../cli/serve.md).
