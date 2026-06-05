# Refresh-Token Rotation

Parsec rotates refresh tokens on every redemption. Each refresh carries
a JTI (per-token unique ID) and a FID (rotation-family ID). Successful
redemption mints a fresh access **and** a fresh refresh that shares
the old FID and carries a new JTI; the old refresh is marked redeemed
in the rotation store and cannot be used again.

A second redemption of the same JTI is treated as a leak: the entire
family is revoked, so every refresh in the rotation chain — past,
present, and future — fails until the recorded expiry passes.

This page covers the rotation contract, the backing store, the
operator-facing knobs, and what to do when a family is revoked.

## Why rotation matters

A long-lived refresh token is a credential. If it leaks (shoulder-surf,
disk image, log spill) the attacker can mint access tokens until the
refresh expires — typically minutes to an hour. Without rotation there
is no way to detect that the leak happened: both the legitimate client
and the attacker hold the same valid token.

With rotation the legitimate client's next refresh attempt presents the
already-redeemed JTI, the server detects the reuse, and Parsec revokes
the family. Sessions break loudly — but only for compromised chains,
and the operator gets a metric and an access-log line that name the
incident.

## Wire shape

The `RefreshTokenResponse` carries the rotated refresh:

```json
{
  "access_token": "...",
  "access_expires_unix": 1735300000,
  "refresh_token": "...",
  "refresh_expires_unix": 1735303600,
  "rotated": true
}
```

Legacy tokens (no JTI on the input refresh) yield a response without
`refresh_token` / `refresh_expires_unix` and with `rotated: false`. A
modern client always uses the returned refresh on the next redemption.

## Rotation store

Two records per chain, scoped to the refresh's natural TTL:

| Record | Purpose |
|---|---|
| `jti` | Marked on successful redemption. A second SET attempt with the same JTI fails — that is the reuse signal. |
| `fid` | Marked on detected reuse. While present, every JTI in the family is rejected without even consulting its own record. |

Two backends:

- **`MemoryRefreshStore`** — single-node; entries live in a map and
  age out on a periodic pruner (default 5 minutes; tune via
  `parsec.Options.MemoryRefreshPruneInterval`).
- **`RedisRefreshStore`** — multi-node; SETNX with a TTL equal to the
  refresh's remaining lifetime. Key layout:

  ```
  <prefix>:refresh:jti:<jti>  → "1"  TTL = exp - now
  <prefix>:refresh:fid:<fid>  → "1"  TTL = exp - now
  ```

`parsec.New` builds the right backend automatically: Redis when
`RedisClient` is configured, in-memory otherwise. Operators can
inject a custom backend by setting `Options.RefreshStore`.

## Operator-facing knobs

| Option | Default | Effect |
|---|---|---|
| `RefreshStore` | nil → auto | Explicit backend |
| `MemoryRefreshPruneInterval` | 5m | How often the in-memory backend reclaims expired records |
| `MaxRefreshTokenTTL` | 1h | Upper bound on every refresh, original or rotated |

`AccessTokenTTL` still bounds the access half (default 5m, clamped to
[1m, 1h]). Rotation does not extend the access TTL beyond the refresh
expiry.

## What to do when a family is revoked

`parsec_refresh_rotations_total{result="reused"}` is the loud
signal — every increment is a confirmed reuse. The corresponding
client will hit `PARSEC_AUTH_DENIED` on its next refresh and need a
fresh credential pair from `CreatePrivate`.

Triage:

1. Find the access log line for the failed redemption
   (`bearer_subject` + `request_id`). The `sub` claim names the
   compromised session.
2. Inspect the channel granted to that session — assume the attacker
   subscribed for the duration the refresh was valid.
3. Issue a new pair via `CreatePrivate` if the legitimate client is
   still online; otherwise let the channel TTL expire naturally.

Family revocation only affects the one rotation chain. Other users
and other refresh families for the same subject continue to work.

## Backward compatibility

Refresh tokens minted by older Parsec versions have no JTI / FID. The
rotation gate detects this and short-circuits to the legacy path:
mint a fresh access only, never touch the store, no `refresh_token`
in the response. Clients upgrading to the rotation surface should
re-`CreatePrivate` once to obtain a JTI-bearing refresh; until they
do, sessions still work but the leak-detection guarantee does not
apply.

## Metrics

- `parsec_refresh_rotations_total{result="rotated"}` — successful
  rotation.
- `parsec_refresh_rotations_total{result="reused"}` — reuse detected;
  family revoked.
- `parsec_refresh_rotations_total{result="family_revoked"}` — caller
  presented a refresh whose family was already revoked.
- `parsec_refresh_rotations_total{result="legacy"}` — pre-rotation
  token; no store interaction.

Cardinality budget: `result` is one of four constants. No subject or
channel labels.
