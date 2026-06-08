# Auth — Parsec

Load when touching `auth/`, key rotation, JWT issuance, scopes, OIDC,
or refresh-token flow.

JWT with `kid`-based key rotation; HS256 default, RS256/EdDSA/ES256/ES384
optional.

## Token types

The `auth/` package mints three token types:

| Type | TTL default | Purpose |
|---|---|---|
| `access` | 5m (clamped [1m, 1h]) | Connect over websocket + subscribe to listed private channels |
| `refresh` | min(channel TTL, 1h) | Exchange at `RefreshToken` RPC for a fresh access + fresh refresh (rotated per redemption; reuse triggers family revoke — see `docs/src/ops/refresh-rotation.md`) |
| `mgmt` | 24h (clamped [1h, 7d]) | `Authorization: Bearer` on the management RPC |

## JOSE header rules

Tokens are compact JWTs. The JOSE header is **fixed per key** —
`{"alg":"<HS256|RS256|EdDSA|ES256|ES384>","kid":"<kid>","typ":"JWT"}`.
The verifier refuses any unknown `alg`/`typ`, refuses tokens without
a `kid`, and refuses tokens whose declared `alg` does not match the
algorithm of the key the `kid` points to (defends against key
confusion). For ECDSA the verifier also rejects signatures whose
length isn't exactly `2 * curve coord size` so a DER-shaped signature
cannot be smuggled in. The `kid` is looked up in a `KeyRing` to fetch
the verifying key material.

## KeyRing

The KeyRing holds N≥1 keys, exactly one with role `active` (the
signer). Each key carries an `Alg` (HS256 / RS256 / EdDSA / ES256 /
ES384); a single ring can mix algorithms. Others are `verify-only`.
Retired keys stop verifying immediately and drop from the next
snapshot.

Asymmetric public keys are exposed via JWKS at `/parsec/jwks.json`
when at least one non-retired asymmetric key is in the ring. HMAC keys
are NEVER exposed there — they are shared secrets, not verifying
material. See
[docs/src/ops/asymmetric-signing.md](../../docs/src/ops/asymmetric-signing.md).

## Persistence

`parsec.Options.StateDir` makes the ring file-backed at
`<StateDir>/keyring.json` (mode `0600`, parent `0700`). Without
StateDir the ring is ephemeral and tokens do not survive a restart.

## Rotation

`parsec keys generate` → `parsec keys promote <kid>` →
`parsec tokens mgmt` (mint a fresh bearer under the new key) → wait the
longest token TTL → `parsec keys retire <old-kid>`. The runbook in
[docs/src/ops/key-rotation.md](../../docs/src/ops/key-rotation.md) has
the full procedure including break-glass.

## Reload

SIGHUP, `parsec keys reload`, or the mtime-poll watcher (5s default,
configurable via `--keyring-poll`).

## Refresh-token rotation

Every refresh carries a `jti` (per-token ID) and an `fid`
(rotation-family ID). `RefreshToken` mints a fresh access + fresh
refresh in the same family; the old `jti` is marked redeemed in a
`RefreshStore` (memory single-node, Redis multi-node). A second
redemption of the same `jti` is reuse — the entire family is revoked.
Legacy refresh tokens without `jti` short-circuit to the old "mint
access only" path. See
[docs/src/ops/refresh-rotation.md](../../docs/src/ops/refresh-rotation.md).

`Manifest`, `RefreshToken` skip the bearer middleware. Every other RPC
requires a valid `mgmt` token signed by a non-retired key.

## OIDC bridge (optional)

When `parsec.Options.OIDCConfig` is non-nil (or the YAML config has
`auth.oidc.issuer` set), parsec also accepts ID tokens issued by the
configured OpenID provider as mgmt bearers. The HMAC verifier runs
first; on failure the OIDC verifier (`auth.OIDCVerifier`, wrapping
`github.com/coreos/go-oidc/v3`) re-checks against the issuer's JWKS.
The composite is `auth.CompositeVerifier`. Successful OIDC tokens
are translated into a synthetic `auth.Claims` with `Typ=TypeMgmt` so
the rate limiter, scope authorizer, and access log treat both kinds
uniformly. `auth.OIDCConfig.Grants` maps IdP group names onto parsec
scope patterns + verb sets, so role-based access lives in the IdP
without parsec needing per-user state. CLI: `parsec login oidc` runs
the device-code flow and writes the ID token to `~/.parsec/credentials`
(0600); `parsec logout` deletes the file. See
[docs/src/ops/oidc.md](../../docs/src/ops/oidc.md) for the full
walkthrough including Google / Okta / Keycloak examples. Deployments
without `OIDCConfig` accept HMAC mgmt tokens only.

## Token claims and scopes

Access and refresh tokens carry an exact-match channel list (`chs`)
and an optional set of pattern-based grants (`scopes`). Each scope is
`{pattern, verbs, deny?}` where pattern uses the channel grammar plus
`*` (single segment) and trailing `**` (multi-segment). Recognized
verbs are `subscribe`, `publish`, `manage`. The authorization check at
every surface is the union of `chs` (exact match — any verb) and
`scopes` (pattern match + verb). Legacy tokens carrying only `chs`
continue to work without modification — the new feature is purely
additive.

Scopes can also be **negative**: a scope with `deny: true` subtracts
its matched (channel, verb) pair from the grant set with **deny-wins**
precedence (matching AWS IAM and similar). The evaluation order is
deny pass → `chs` pass → allow pass; any matching deny rejects the
request even if `chs` or an allow scope would otherwise have permitted
it. Deny patterns are visible in the token (signed but not encrypted),
so do not encode secret channel names in a deny pattern. The manifest
exposes `deny_supported: true` and `scope_precedence: "deny-wins"`.
See `docs/src/channels/acl.md` for the grammar, worked example, and
the security note.
