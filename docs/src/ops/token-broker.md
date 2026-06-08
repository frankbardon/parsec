# Token Broker

The token broker is the policy point for issuing user-facing connection
tokens. A backend service authenticates the end user, asks the broker
for a token scoped to a list of channels, the broker consults an
`Authorizer`, and emits a JWT the browser uses to connect via WebSocket
or HTTP-streaming.

The broker is opt-in: deployments wire `Options.TokenBrokerHandler`
when they want delegated issuance and revocation. The library facade
(`parsec.New`) does not require it — manifest-direct embeddings can
mint tokens via `auth.Issuer` directly.

## Routes

When `Options.TokenBrokerHandler = broker.Handler()` is set, the
following routes mount under `/parsec`:

| Path | Body | Returns |
|---|---|---|
| `POST /parsec/token` | `IssueRequest { channels, ttl }` | `IssueResponse { token, token_id, expires_at, channels_granted, channels_denied }` |
| `POST /parsec/token/delegate` | `DelegateRequest { on_behalf_of, channels, lifetime_seconds }` | `IssueResponse` |
| `POST /parsec/revoke` | `RevokeRequest { token_id?, user_id?, reason? }` | `{ "ok": true }` |

Every route requires `Authorization: Bearer <user-token>`. The bearer
is passed verbatim to the broker's `Authenticator` — Parsec does no
signature verification on the user-auth bearer.

## Delegated issuance

A trusted backend service can mint a token "on behalf of" a different
user by POSTing to `/parsec/token/delegate`. The service's own bearer
is the requester; `on_behalf_of` names the target user.

Configure `Options.DelegateAuthorizer` to gate which services may
delegate to which users:

```go
b, _ := tokenbroker.New(tokenbroker.Options{
    Issuer:        issuer,
    Authorizer:    authorizer,
    Authenticator: authn,
    DelegateAuthorizer: func(ctx context.Context, svc, target tokenbroker.UserID) error {
        if svc == "svc-notifier" && strings.HasPrefix(string(target), "user-") {
            return nil
        }
        return tokenbroker.ErrUnauthenticated
    },
})
```

When `DelegateAuthorizer` is nil, the broker accepts any delegation
the `Authenticator` admits — fine for single-tenant deployments,
risky in multi-tenant ones.

The audit log captures both the delegator and the on-behalf-of
identity in every entry so investigations can trace who minted what.

## Revocation

Token revocation requires two pieces:

1. A `RevocationStore` that survives across nodes.
2. `Options.RevocationStore` on `parsec.New` so the subscribe authorizer
   consults the store before allowing a private subscribe.

### Stores

| Store | Use case |
|---|---|
| `tokenbroker.NewMemoryRevocations()` | Single-node dev/test. Entries age out after `MaxTTL` (default 24h); start the pruner with `store.StartPruner(ctx, interval)` to reclaim map memory. |
| `tokenbroker.NewRedisRevocations(client)` | Multi-node production. Revocation entries SET with EX `MaxTTL`. Tune with `WithMaxTTL(d)`. Keys live under `<prefix>:tb:revoked:*` and `<prefix>:tb:user_revoked_at:*`. |

```go
store := tokenbroker.NewRedisRevocations(redisClient).
    WithKeyPrefix("parsec").
    WithMaxTTL(36 * time.Hour) // cover the longest mgmt TTL + a margin

b, _ := tokenbroker.New(tokenbroker.Options{
    Issuer:        issuer,
    Authorizer:    authorizer,
    Authenticator: authn,
    Revocations:   store,
})

p, _ := parsec.New(parsec.Options{
    KeyRing:            ring,
    TokenBrokerHandler: b.Handler(),
    RevocationStore:    store, // SAME store — broker and subscribe path share it
})
```

When `Options.RevocationStore` is left nil, the broker can still mark
tokens revoked in its own copy, but the subscribe authorizer never
consults that copy — connections accepted before the revoke continue
to subscribe. The manifest exposes `revocation_store_enabled: false`
to flag this gap to operators.

### Scopes

- **Single-token revoke** (`token_id`): the named token cannot be used
  to subscribe again. Re-subscribe attempts return
  `PARSEC_AUTH_DENIED`.
- **User blanket revoke** (`user_id`): every token issued to the user
  *before* the revoke moment is rejected. Tokens minted after the
  revoke remain valid — operators who want a full lock-out also need to
  stop minting new tokens for the user upstream.

The subscribe authorizer reads the token's `jti` (added to every
access token automatically) and `sub` claims. Tokens minted before
this PR landed do not carry a `jti` and cannot be single-token revoked;
those tokens still age out naturally at their `exp` claim and the
user-blanket revoke still applies.

## Manifest exposure

The public Manifest now surfaces both states so SDKs and dashboards
can discover broker availability without sniffing routes:

```json
{
  "kind": "parsec.manifest",
  "payload": {
    "token_broker_enabled": true,
    "revocation_store_enabled": true
  }
}
```

## Audit log

Pass an `AuditLogger` to capture every successful issuance:

```go
b, _ := tokenbroker.New(tokenbroker.Options{
    // ... Issuer / Authorizer / Authenticator
    AuditLog: tokenbroker.AuditLoggerFunc(func(ctx context.Context, e tokenbroker.AuditEntry) {
        slog.Info("token issued",
            "token_id", e.TokenID,
            "user", e.IssuedTo,
            "delegator", e.Delegator,
            "channels", e.Channels,
            "expires_at", e.ExpiresAt,
        )
    }),
})
```

Audit entries are best-effort observation — they fire after the JWT
is signed and the revocation store is updated. A panicking logger
will not undo an issuance.

## Operator-side CLI

The Twirp service exposes two operator-driven revoke RPCs gated by the
mgmt bearer. The CLI is the canonical front door:

```bash
# Single token (the access token's jti claim, which is the same value
# the broker returns in IssueResponse.token_id).
$ parsec tokens revoke <token-id> --user <user-id> --reason "compromised"

# Blanket-revoke every token issued to a user before now.
$ parsec tokens revoke-user <user-id> --reason "password reset"
```

`PARSEC_TOKEN` (or `--token`) supplies the mgmt bearer the same way
every other `parsec` subcommand consumes one. The CLI is just a thin
wrapper around the RPC; backends that need to call from code should
hit `RevokeToken` / `RevokeUser` directly via the generated Twirp
client.

`PARSEC_INVALID_ARGUMENT` is returned when `Options.RevocationStore` is
nil — the operator-facing path refuses to silently no-op so a missing
wiring is discovered loudly. The user-facing `/parsec/revoke` HTTP
route remains the right surface for end-user-driven flows (e.g. a
"sign me out everywhere" button) because it consumes the user's own
bearer via the broker's `Authenticator`.

## Operational runbook — revoking a compromised token

1. Reach for the issuance audit log to find the `token_id`.
2. `parsec tokens revoke <id> --user <user-id> --reason "compromised"`
   (or the raw `curl -X POST -H "Authorization: Bearer $MGMT" -d '{"token_id":"<id>","reason":"compromised"}' https://parsec.example.com/twirp/parsec.ParsecService/RevokeToken` Twirp call).
3. Verify the revocation is visible by reissuing a fresh token for the
   same user — the new token's `token_id` will differ and the old one
   will be rejected on its next subscribe.
4. If the compromise scope is broader than one token, blanket-revoke
   the user with `parsec tokens revoke-user <id> --reason "..."`.
