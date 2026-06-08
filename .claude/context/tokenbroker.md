# Token broker — Parsec

Load when changing `tokenbroker/`, revocation paths, or delegated
issuance.

The token broker is the policy point for user-facing connection tokens.
The library mints tokens via `auth.Issuer`; the broker sits in front
and adds:

- Channel ACL via a pluggable `Authorizer` (RoleAuthorizer / AllowAll /
  custom).
- Delegated issuance (`/parsec/token/delegate`) where a backend service
  mints "on behalf of" a user; both identities land in the audit log.
- Revocation (`/parsec/revoke`) backed by `RevocationStore`. Two impls
  ship: `MemoryRevocations` (single-node; entries age out past
  `MaxTTL`; call `StartPruner` to reclaim memory) and `RedisRevocations`
  (multi-node; SET with EX `MaxTTL`).

Every access token now carries a unique `jti` claim so single-token
revocation works. Subscribe-side revocation is plumbed via
`parsec.Options.RevocationStore` — wired identically on the broker
side, the subscribe authorizer consults the store on every private
channel attempt and denies revoked tokens with PARSEC_AUTH_DENIED. A
deployment that exposes `/parsec/revoke` but leaves
`Options.RevocationStore` nil cannot deny mid-flight tokens; the
manifest's `revocation_store_enabled` flag surfaces that gap.

See [docs/src/ops/token-broker.md](../../docs/src/ops/token-broker.md).

## Operator surface

Operators reach the store via the Twirp service surface:
`RevokeToken(token_id, user_id?, reason?)` and
`RevokeUser(user_id, reason?)`. Both are gated by the mgmt bearer
(distinct from the user-facing `/parsec/revoke` HTTP route, which
authenticates with the user's own bearer via the broker's
`Authenticator`). The CLI ships `parsec tokens revoke` and
`parsec tokens revoke-user`. Both RPCs return PARSEC_INVALID_ARGUMENT
when no `RevocationStore` is wired so misconfiguration fails loudly
instead of no-op'ing.
