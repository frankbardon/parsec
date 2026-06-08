# parsec tokens

Token operations. The subcommands split along auth boundary:

- `refresh` is **public** — the refresh token in the body authenticates
  the call. Use it to re-mint an access token without holding a mgmt
  bearer.
- `mgmt`, `revoke`, and `revoke-user` require a valid mgmt bearer.
  `mgmt` is used during key rotation; `revoke` / `revoke-user` are the
  operator-facing front door to the `RevocationStore`.

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

## tokens revoke

Mark a single access token revoked by its `jti` claim (the same value
the token broker returns as `token_id` in its issuance response). The
subscribe authorizer consults the store on every private subscribe and
denies revoked tokens with `PARSEC_AUTH_DENIED`.

```bash
parsec tokens revoke <token-id> --user <user-id> --reason "compromised"
```

| Flag | Default | Notes |
|---|---|---|
| `--user`, `-u` | (empty) | Optional user-id recorded with the entry for audit |
| `--reason`, `-r` | (empty) | Optional free-text reason recorded with the entry for audit |

Output:

```json
{
  "type": "parsec.token.revoked",
  "payload": {
    "token_id": "abcd1234...",
    "user_id":  "user-42",
    "reason":   "compromised"
  }
}
```

The call returns `PARSEC_INVALID_ARGUMENT` when the server has no
`RevocationStore` wired (`parsec.Options.RevocationStore` left nil).
That's a deliberate loud failure — silently no-op'ing here would mask
a security gap.

## tokens revoke-user

Blanket-revoke every token previously issued to a user. The store
records the wall-clock cutoff; tokens minted **after** the call
remain valid (use this when a user re-authenticates after a
compromise — old sessions die, new ones live).

```bash
parsec tokens revoke-user <user-id> --reason "password reset"
```

| Flag | Default | Notes |
|---|---|---|
| `--reason`, `-r` | (empty) | Optional free-text reason recorded with the entry for audit |

See [token broker](../ops/token-broker.md#operator-side-cli) for the
operational runbook.
