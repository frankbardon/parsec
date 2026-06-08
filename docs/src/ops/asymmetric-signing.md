# Asymmetric Signing (RS256 / EdDSA / ES256 / ES384)

Parsec's default signer is HS256 — a single 32-byte HMAC secret per
key. Symmetric is fast, small on the wire, and easy to rotate when the
verifier sits in the same process. It does NOT fit deployments where
verifying tokens needs to happen somewhere parsec is not (a CDN edge,
a separate API gateway, an external partner). Sharing the HMAC secret
to those parties would give them the ability to *mint* tokens too.

This page covers Parsec's RS256, EdDSA, and ECDSA (ES256, ES384)
support: how to add an asymmetric key, how to publish the public
material via the JWKS endpoint, and the trade-offs.

## When to choose which alg

| Alg | Use when |
|---|---|
| `HS256` | All verifiers are Parsec processes you control. Smallest tokens, simplest rotation. (Default.) |
| `EdDSA` | You need a public key for external verifiers and you control the consumer libraries. Smaller keys (32-byte private seed), faster signing than RS256. |
| `ES256` | NIST-curve interop required (FIPS-leaning environments, hardware modules that don't speak Ed25519). 64-byte signatures, P-256 keys, fast verification. |
| `ES384` | Same as ES256 but with a P-384 curve when a 192-bit security level is required by policy. 96-byte signatures. |
| `RS256` | You need broad ecosystem interop (most enterprise SSO / JWKS libraries assume RSA). Largest keys (2048+ bit modulus), larger signatures, slower signing. |

You can mix algorithms in one ring — Parsec dispatches per key. A
common deployment runs HS256 for internal mgmt tokens and Ed25519 (or
ES256) for client tokens consumed by an edge verifier.

## Adding an asymmetric key

```bash
# Default (back-compat) is still HS256 — same as before.
parsec keys generate

# Ed25519 (joins as verify-only):
parsec keys generate --alg eddsa

# RS256 with the default 2048-bit modulus:
parsec keys generate --alg rs256

# ECDSA on P-256 / P-384:
parsec keys generate --alg es256
parsec keys generate --alg es384
```

Promote to active when you are ready to start signing with it:

```bash
parsec keys list                 # find the new key id
parsec keys promote <new-kid>
```

The previously-active key transitions to `verify-only` so existing
tokens stay valid until they expire. Retire the old key after the
longest token TTL elapses (same procedure as
[key rotation](./key-rotation.md)).

## JWKS endpoint

Once any non-retired key in the ring is asymmetric, Parsec serves a
JWKS document (RFC 7517) at:

```
GET /parsec/jwks.json
```

The handler returns one JWK per asymmetric key. HMAC keys are
**never** exposed — they are shared secrets, not verifying material.

A minimal Ed25519 JWKS entry looks like:

```json
{
  "kty": "OKP",
  "kid": "k-9a3f1c0b1e22",
  "alg": "EdDSA",
  "use": "sig",
  "crv": "Ed25519",
  "x": "<base64url public key>"
}
```

ECDSA JWKS entries carry `kty=EC` with both coordinates of the public
point. The `x` and `y` fields are fixed-width per curve (32 bytes for
P-256, 48 bytes for P-384) before base64url encoding:

```json
{
  "kty": "EC",
  "kid": "k-1f3b8a04c011",
  "alg": "ES256",
  "use": "sig",
  "crv": "P-256",
  "x": "<base64url X coordinate, 32 bytes>",
  "y": "<base64url Y coordinate, 32 bytes>"
}
```

The response is cacheable (`Cache-Control: public, max-age=300`).
External verifiers should respect that header and re-fetch after the
TTL or on a kid lookup miss.

`/parsec/jwks.json` is unauthenticated by design — verifying parties
must be able to reach it without a Parsec credential. Mount it behind
a CDN with appropriate firewalling if reachability needs to be
controlled.

When the ring contains only HMAC keys, the route returns 404. There is
no entry to expose, so probing yields no information.

## Wire shape — what changes in the token

Every JWT carries `alg` in its header. Parsec writes one of `HS256`,
`RS256`, `EdDSA`, `ES256`, or `ES384`. The verifier checks the header
`alg` against the algorithm of the key it points to (`kid`); a
mismatch is rejected with `PARSEC_AUTH_DENIED` to defeat key-confusion
attacks. There is no algorithm negotiation — the kid uniquely
determines the signing alg.

ECDSA signatures are written in the JOSE fixed-width form (RFC 7515
§3.4): the raw `r||s` integers padded to the curve's coordinate size
(32 bytes for P-256, 48 bytes for P-384), NOT the ASN.1/DER wrapper
that ECDSA APIs typically emit. The verifier rejects any signature
whose length isn't exactly `2 * coord_size` so a DER-shaped signature
cannot be smuggled in. Other libraries that produce DER need to
re-encode to fixed-width when interoperating.

## Persistence

The keyring snapshot format version bumps from `1` to `2` to carry
the per-key `alg` and `private_pem` (PKCS#8 for RS256, EdDSA, and
both ECDSA curves). Loaders accept both versions:

- v1 (or v2 with `alg=""`) is treated as HS256, falling back to the
  `secret_hex` field. Legacy on-disk rings load without operator
  intervention.
- v2 with `alg=RS256` / `alg=EdDSA` / `alg=ES256` / `alg=ES384`
  decodes `private_pem` and reconstructs the typed key. For ECDSA the
  loader cross-checks that the encoded curve matches the declared alg
  (P-256 ↔ ES256, P-384 ↔ ES384) — a mismatch refuses to load.

File mode + parent dir mode are unchanged (`0600` / `0700`). PKCS#8
encoding means the on-disk blob carries the algorithm internally, so
the loader cross-checks the declared `alg` against the parsed key
type — a mismatch refuses to load the ring rather than guess.

## What is not yet supported

- Algorithm-per-token-type (e.g. mgmt = HS256, access = EdDSA). The
  active key dictates the alg for every issuance.
- ECDSA on P-521 (ES512). JOSE defines it, but parsec pins the
  supported curves to P-256 and P-384; larger curves bring
  variable-length JOSE signatures and no compelling operator
  benefit.
- JWKS rotation hints (the `alg` field is per-key; we do not currently
  emit `x5c` or chain metadata).

## Metrics and audit

`parsec_token_verifications_total{type, result}` is unchanged. The
metric does not break down by alg — adding it would create a
five-cell label set per token type for limited operator benefit. The
access log records the kid that verified each request; correlate
against `parsec keys list` to see which alg signed it.
