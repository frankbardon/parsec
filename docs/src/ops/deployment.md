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

Point `Options.RedisClient` (or the YAML `redis.addr` field) at a
shared Redis and the broker, channel registry, keyring, DLQ, and rate
limiter all switch to their Redis-backed implementations. Multiple
Parsec nodes then share the same truth about open channels, keys, and
sink failures. See [key rotation](key-rotation.md#multi-node-deployments)
for the rotation walkthrough.

## Required state

| Path | What | Survives restart? |
|---|---|---|
| `<state-dir>/keyring.json` | HMAC signing keys | Yes |
| In-memory channel map | Open channels, private records | No |
| In-memory subscriber set | Live websocket connections | No |

The manifest reports `"persistence": "in-memory"` so clients can
discover the stance without reading docs. The contract: a restart wipes
every channel, every subscriber must reconnect, every private channel
must be re-created with fresh tokens.

If that is a problem for your use case, Parsec is the wrong primitive
— use a real durable queue.

## `--state-dir`

Always pass it in production. Without `--state-dir`, the keyring is
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
| `PARSEC_KEYRING_POLL` | `5s` | mtime-poll interval. `0` disables polling. |
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
| `/healthz` | Liveness probe. Returns 200 once `node.Run` has succeeded. |
| `/manifest` | Descriptor envelope — surfaces, sinks, version. |

Centrifuge's own metrics are exposed under
`broker.Node().Metrics(...)`; mount them on your own `/metrics`
handler if you want Prometheus.

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
