# `parsec serve`

Boot the broker and the HTTP service (Twirp, websocket, SSE, healthz)
on one address.

```bash
./bin/parsec serve --addr :8000 --state-dir /var/lib/parsec
# parsec serve: loaded keyring from /var/lib/parsec/keyring.json (active=k0)
# parsec serve: bootstrap mgmt token (expires 2026-05-23 18:04:11 UTC):
# eyJhbGciOiJIUzI1NiIs...
```

## What it does

`serve` is the only long-running subcommand. It:

1. Builds a `*parsec.Parsec` with the flag-driven `Options`.
2. Loads (or bootstraps) the keyring at
   `<state-dir>/keyring.json`. Without `--state-dir` the ring is
   ephemeral and tokens die on restart.
3. Mints a bootstrap mgmt token under the active key and prints it to
   stderr — copy it before the server scrolls it off.
4. Starts the Centrifuge node, the channel-manager sweeper, the
   manager-to-broker event bridge, and (when persistent) the
   keyring-file watcher.
5. Serves Twirp at `/twirp/parsec.ParsecService/`, the websocket at
   `/connection/websocket`, SSE at `/sse`, and the manifest at
   `/manifest`.

Stop with `SIGINT` / `SIGTERM`. The broker drains over a 5-second
shutdown grace period before the process exits.

## Operator-disabled auth

`--no-auth` removes the bearer middleware from the management RPC. The
flag exists for local development against a throwaway broker; use it
anywhere production-shaped and an attacker can mutate channels at will.
Parsec logs a loud warning at boot to make accidents loud.

## Persistent state

`--state-dir` controls the keyring file path. The directory is created
with mode `0700`; the file with mode `0600`. Channel records are NOT
persisted — only the HMAC ring is. See
[deployment](../ops/deployment.md) for the full persistence stance and
[key rotation](../ops/key-rotation.md) for the rotation runbook.

## Reference

```text
NAME:
   parsec serve - Boot the parsec broker and HTTP/Twirp service

USAGE:
   parsec serve [options]

OPTIONS:
   --addr string, -a string  Listen address (host:port) (default: ":8000")
   --state-dir string        Directory holding keyring.json (created with 0700 if missing). If unset, the keyring is ephemeral. [$PARSEC_STATE_DIR]
   --mgmt-subject string     Subject (sub claim) for the bootstrap mgmt token printed at boot (default: "operator") [$PARSEC_MGMT_SUBJECT]
   --mgmt-ttl duration       Lifetime of the bootstrap mgmt token printed at boot (default: 24h0m0s) [$PARSEC_MGMT_TTL]
   --keyring-poll duration   Interval between keyring.json mtime checks. 0 disables polling. (default: 5s) [$PARSEC_KEYRING_POLL]
   --no-auth                 Disable bearer auth on the management RPC. DANGEROUS — local development only. [$PARSEC_NO_AUTH]
   --help, -h                show help

GLOBAL OPTIONS:
   --json  Output the parsec manifest as a descriptor envelope
```

## See also

- [channels](channels.md) — operate on the broker once it is up.
- [Key rotation runbook](../ops/key-rotation.md).
- [Deployment](../ops/deployment.md).
