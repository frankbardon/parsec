# OIDC Bridge for Management Authentication

Parsec's management RPC accepts two kinds of bearer tokens:

1. **HMAC mgmt tokens** — minted by `parsec tokens mgmt` (or
   `IssueMgmt`) and signed by the active key in the parsec keyring.
2. **OIDC ID tokens** — issued by a corporate IdP (Okta, Auth0,
   Google Workspace, Keycloak) and verified against the issuer's
   JWKS endpoint at request time.

OIDC is opt-in. When `auth.oidc.issuer` is empty (the default), the
only accepted mgmt tokens are HMAC-signed parsec tokens.

## Why OIDC?

Parsec HMAC tokens are well-suited to service-to-service automation:
short-lived, key-rotation-aware, and minted by the parsec instance
itself. Human operators are different — corporate SSO is the
expected entry point, and provisioning a per-operator HMAC token
just to call the management RPC adds operational friction.

Mounting OIDC alongside HMAC means operators authenticate with the
same identity their IT department already manages, while automation
keeps using the existing token issuer.

## Architecture

```text
                  +-----------------+
  bearer token -> | bearer middleware| --> twirp handler
                  +-----------------+
                          |
                          v
                  +-----------------+
                  | CompositeVerifier|
                  +-----------------+
                    /            \
                   v              v
            +--------+      +-----------+
            |  HMAC  |      |   OIDC    |
            +--------+      +-----------+
            (keyring)       (issuer JWKS)
```

The bearer middleware tries HMAC first. On any failure (malformed,
expired, signature mismatch, unknown kid), it falls through to the
OIDC verifier, which:

1. Re-fetches the IdP's JWKS if the cached keys are stale.
2. Verifies the token's signature against the JWKS.
3. Enforces `exp`, `iat`, `aud`, and `iss`.
4. Translates the payload into a synthetic `auth.Claims`:
    - `sub` <- the claim named by `subject_claim` (defaults to `sub`)
    - `typ` <- `"mgmt"`
    - `scopes` <- derived from the IdP groups via `auth.oidc.grants`

The synthetic claims object has the same shape as an HMAC-issued
mgmt token, so downstream code (the rate limiter, scope authorizer,
access log) does not need an OIDC-specific code path.

## Configuration

OIDC is configured via the YAML config file under `auth.oidc`:

```yaml
auth:
  oidc:
    issuer: https://accounts.google.com
    audience: parsec-prod
    # Optional claim mapping. Defaults: sub -> subject; groups -> scopes.
    subject_claim: email
    scopes_claim: groups
    # Map IdP group names to parsec verbs / channel patterns.
    grants:
      - if_group: parsec-admins
        scope: "*:**"
        verbs: [subscribe, publish, manage]
      - if_group: parsec-readers
        scope: "public:**"
        verbs: [subscribe]
```

| Key | Purpose | Default |
|---|---|---|
| `issuer` | OpenID issuer URL. Empty disables OIDC. | (none) |
| `audience` | Expected `aud` claim — the client identifier registered with the IdP. Required when `issuer` is set. | (none) |
| `subject_claim` | Which claim becomes `Claims.Sub`. Common: `email`, `preferred_username`, `sub`. | `sub` |
| `scopes_claim` | Which claim carries the operator's group memberships. | `groups` |
| `grants[].if_group` | Group name to match against the token's `scopes_claim`. Case-sensitive. | (required) |
| `grants[].scope` | parsec scope pattern to grant (channel grammar + `*` / `**`). | (required) |
| `grants[].verbs` | Subset of `[subscribe, publish, manage]`. | (required) |

`auth.oidc.issuer` accepts the same `${ENV_VAR}` interpolation as
every other string field in the config.

## Setup walkthroughs

The examples below show the placeholder IdP-side settings needed to
make parsec a registered client. Adjust `${PARSEC_HOSTNAME}` and
group names to your deployment.

### Google Workspace

1. In Google Cloud Console, create an OAuth 2.0 client of type
   "Web application" or "Desktop" (for device-code flow).
2. Set the client ID as the parsec audience.
3. Add the `https://accounts.google.com/.well-known/openid-configuration`
   issuer to parsec's config:

   ```yaml
   auth:
     oidc:
       issuer: https://accounts.google.com
       audience: ${GOOGLE_CLIENT_ID}
       subject_claim: email
       # Google does not ship group claims by default. Pair this
       # with the Cloud Identity API or push groups through a
       # custom claim during sign-in.
       scopes_claim: groups
       grants:
         - if_group: parsec-admins@example.com
           scope: "*:**"
           verbs: [subscribe, publish, manage]
   ```

4. On the operator's laptop:

   ```bash
   parsec login oidc \
     --issuer https://accounts.google.com \
     --client-id "$GOOGLE_CLIENT_ID"
   ```

   The CLI prints a verification URL and a user code. Open the URL,
   sign in, paste the code — the CLI receives the ID token and
   writes it to `~/.parsec/credentials`.

### Okta

1. In Okta, create an OIDC application of type "Native" (device
   grant supported).
2. Enable "Device Authorization Grant" under the General tab.
3. Pin a custom claim that includes the user's group memberships
   (e.g. `groups` <- `getFilteredGroups(...)`).

   ```yaml
   auth:
     oidc:
       issuer: https://${OKTA_DOMAIN}/oauth2/default
       audience: ${OKTA_CLIENT_ID}
       subject_claim: email
       scopes_claim: groups
       grants:
         - if_group: parsec-admins
           scope: "*:**"
           verbs: [subscribe, publish, manage]
         - if_group: parsec-readers
           scope: "public:**"
           verbs: [subscribe]
   ```

4. Operator login:

   ```bash
   parsec login oidc \
     --issuer https://${OKTA_DOMAIN}/oauth2/default \
     --client-id "$OKTA_CLIENT_ID"
   ```

### Keycloak

1. In the realm of your choice, create a client with
   "Standard Flow" + "OAuth 2.0 Device Authorization Grant"
   enabled.
2. Set the client's access type to "public" (or "confidential" with
   a client secret — pass `--client-secret` to `parsec login oidc`
   in that case).
3. Map the user's groups onto a claim named `groups`.

   ```yaml
   auth:
     oidc:
       issuer: https://${KEYCLOAK_HOST}/realms/${REALM}
       audience: ${KEYCLOAK_CLIENT_ID}
       subject_claim: preferred_username
       scopes_claim: groups
       grants:
         - if_group: /parsec-admins
           scope: "*:**"
           verbs: [subscribe, publish, manage]
   ```

4. Operator login:

   ```bash
   parsec login oidc \
     --issuer https://${KEYCLOAK_HOST}/realms/${REALM} \
     --client-id "$KEYCLOAK_CLIENT_ID" \
     --client-secret "$KEYCLOAK_CLIENT_SECRET"   # optional
   ```

## CLI usage

After `parsec login oidc` completes, the persisted ID token at
`~/.parsec/credentials` is picked up by subsequent CLI invocations
that read `PARSEC_TOKEN` from `--token`. To inspect the file:

```bash
cat ~/.parsec/credentials
```

To clear it (sign out):

```bash
parsec logout
```

The CLI does NOT refresh expired ID tokens automatically; when the
token expires, run `parsec login oidc` again.

## Verifying the bridge is live

The manifest exposes two fields the operator can query:

```bash
curl -s http://localhost:8000/manifest | jq '.payload | {auth_oidc_enabled, auth_oidc_issuer}'
```

`auth_oidc_enabled: true` and a populated `auth_oidc_issuer` confirm
parsec discovered the issuer's JWKS at boot.

## Security notes

- Parsec verifies the token via the IdP's JWKS only — there is no
  RFC 7662 token introspection. If the operator's IdP revokes the
  token mid-session, parsec will keep accepting it until `exp`.
  Operators with strict revocation requirements should configure
  short `exp` lifetimes on the IdP side.
- The OIDC verifier is constructed at boot. The JWKS is re-fetched
  by the underlying go-oidc library when the cached keys can't
  verify a token (key rotation is handled transparently).
- An operator whose groups don't match any grant still authenticates
  but receives an empty scope set — they can read the manifest but
  not subscribe / publish / manage. Configure a default-deny grant
  if you want stricter behavior.
- A deployment without `auth.oidc.issuer` accepts HMAC tokens only —
  the OIDC verifier is not constructed and the bearer middleware skips
  the JWKS path entirely.

## Cross-references

- [Key rotation runbook](key-rotation.md) — the HMAC side of the
  story; OIDC operates orthogonally.
- [Channel ACLs and scopes](../channels/acl.md) — the grammar
  consumed by `auth.oidc.grants[].scope`.
