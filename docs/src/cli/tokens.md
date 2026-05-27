# parsec tokens

Token operations. The subcommands split along auth boundary:

- `refresh` is **public** — the refresh token in the body authenticates
  the call. Use it to re-mint an access token without holding a mgmt
  bearer.
- `mgmt` requires a valid mgmt bearer. Use it during key rotation, when
  the previous bearer is signed by a key you are about to retire.

## Flags

| Flag | Env | Default |
|---|---|---|
| `--server` | `PARSEC_SERVER` | `http://localhost:8000` |
| `--token`  | `PARSEC_TOKEN`  | (empty — required for `mgmt`) |

## tokens refresh

Exchange a refresh token for a fresh access token. The new access token
cannot outlive the refresh expiry.

```bash
parsec tokens refresh "<refresh-token>"
```

Output (descriptor envelope):

```json
{
  "type": "parsec.token.refreshed",
  "payload": {
    "access_token": "eyJhbGciOi...",
    "expires_at":   "2026-05-22T18:14:11Z"
  }
}
```

## tokens mgmt

Mint a new mgmt bearer signed by the active key. The intended use is
mid-rotation: after `parsec keys promote <new-kid>`, the operator's
current bearer is still signed by the old key. Calling `tokens mgmt`
issues a fresh bearer under the new key so the old one can safely
retire.

```bash
parsec tokens mgmt --subject ops-bot --ttl 24h
```

| Flag | Default | Notes |
|---|---|---|
| `--subject`, `-s` | `operator` | Subject (`sub` claim) |
| `--ttl` | `24h` | Clamped to [1h, 7d] |

Output:

```json
{
  "type": "parsec.token.mgmt",
  "payload": {
    "mgmt_token": "eyJhbGciOi...",
    "expires_at": "2026-05-23T17:08:42Z"
  }
}
```

The new bearer typically goes straight into `PARSEC_TOKEN`:

```bash
export PARSEC_TOKEN=$(parsec tokens mgmt | jq -r .payload.mgmt_token)
```

See [key rotation](../ops/key-rotation.md) for the full rotation runbook.
