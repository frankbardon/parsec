# parsec keys

Manage HMAC signing keys. The keyring lives at
`<state-dir>/keyring.json` (file-backed) or in Redis (multi-node). The
CLI never edits the file directly except for `export` / `import` — every
other subcommand routes through the mgmt RPC so the server is the
single writer.

## Flags

| Flag | Env | Default |
|---|---|---|
| `--server` | `PARSEC_SERVER` | `http://localhost:8000` |
| `--token`  | `PARSEC_TOKEN`  | (empty — required) |

## keys list

```bash
parsec keys list
```

Returns every key in the ring with its role (`active`, `verify-only`,
`retired`), creation timestamp, and short fingerprint. Retired keys
appear once in the result and then drop on the next snapshot.

## keys generate

```bash
parsec keys generate
```

Mints a new 32-byte HMAC secret and joins it to the ring as
`verify-only`. The new key does NOT sign tokens yet — `promote` does
that. The kid is returned in the descriptor envelope.

## keys promote

```bash
parsec keys promote <kid>
```

Makes `<kid>` the active signer. The previous active key demotes to
`verify-only`, so tokens already issued under it keep verifying until
they expire.

## keys retire

```bash
parsec keys retire <kid>
parsec keys retire <kid> --notify https://parsec.eu-west.example.com:8000 \
                          --notify https://parsec.ap-south.example.com:8000
```

Stops the named key from verifying. Operators must wait the longest
in-flight token TTL (default 24h for mgmt) before retiring a key, or
existing tokens reject early.

`--notify <peer-url>` POSTs a `ReloadKeys` to each listed peer after
the local retire succeeds. Peers must share the keyring (via the
`parsec-keys-sync` daemon or a shared Redis) AND trust the bearer this
CLI presents. Individual peer failures are surfaced in the envelope but
never roll back the local retire — propagation is best-effort.

## keys reload

```bash
parsec keys reload
```

Force the server to re-read its keyring file. Functionally identical to
sending `SIGHUP` to the parsec process. Use when the mtime-poll watcher
is disabled (`--keyring-poll 0`).

## keys export

```bash
# from a file-backed ring
parsec keys export --state-dir /var/lib/parsec -o snapshot.json

# from a Redis-backed ring
parsec keys export --redis-addr redis://localhost:6379 \
                   --redis-key-prefix parsec-us-east -o snapshot.json
```

Emits the wire-stable `auth.Snapshot` JSON. The output of `export` is
the input of `import` — operators in different regions shuttle the file
to share signing keys without a live RPC route between them.

Source flags `--state-dir` and `--redis-addr` are mutually exclusive;
exactly one must be set. Output goes to stdout unless `--out` is given.

## keys import

```bash
parsec keys import --state-dir /var/lib/parsec snapshot.json
parsec keys import --redis-addr redis://localhost:6379 \
                   --redis-key-prefix parsec-eu-west snapshot.json
```

Reads a snapshot file and merges it into the local store. Existing
non-retired kids in the local ring are preserved; matching kids are
overwritten with the snapshot's role. The active key in the snapshot
becomes the active key locally; if multiple actives would result, the
import aborts with a coded error so the operator can resolve the
conflict.

After import, run `parsec keys reload` (or send SIGHUP) on every node so
in-process verifiers pick up the new keys without a restart.

See [key rotation](../ops/key-rotation.md) for the full rotation runbook
and [multi-region](../ops/multi-region.md) for cross-region propagation.
