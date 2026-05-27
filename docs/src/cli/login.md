# parsec login / parsec logout

Authenticate against an OIDC identity provider via the device-code flow
(RFC 8628). The resulting ID token is persisted at
`~/.parsec/credentials` (mode `0600`) and picked up automatically by the
CLI's `--token` resolution — every subsequent `parsec` invocation
presents it as the mgmt bearer.

The server side of the bridge is documented at
[OIDC bridge](../ops/oidc.md). The IdP must implement
`device_authorization_endpoint` for this flow to work; Google, Okta,
Keycloak, Auth0, and Azure AD all do.

## login oidc

```bash
parsec login oidc \
  --issuer https://accounts.google.com \
  --client-id "<your-oauth-client-id>"
```

| Flag | Env | Default |
|---|---|---|
| `--issuer` | `PARSEC_OIDC_ISSUER` | (required) |
| `--client-id` | `PARSEC_OIDC_CLIENT_ID` | (required) |
| `--client-secret` | `PARSEC_OIDC_CLIENT_SECRET` | (empty — public clients) |
| `--scope` | — | `openid email profile groups` |
| `--timeout` | — | `5m` |

The flow:

1. Parsec fetches the issuer's `/.well-known/openid-configuration` and
   extracts `device_authorization_endpoint` + `token_endpoint`.
2. POSTs to the device-auth endpoint with the client id; receives a
   `user_code`, a `verification_uri`, and a polling interval.
3. Prints the URI + code to stdout. Open the URL in a browser, enter
   the code, and approve the request.
4. Polls the token endpoint at the IdP-advertised interval until the
   operator approves, the device code expires, or `--timeout` elapses.
5. Writes the resulting ID token to `~/.parsec/credentials`.

The CLI verifies the token against the IdP's JWKS as a sanity check.
The server re-verifies on every RPC, so a tampered local file just
results in 401s.

## logout

```bash
parsec logout
```

Removes `~/.parsec/credentials`. Subsequent CLI invocations require
`--token` (or `PARSEC_TOKEN`) to authenticate again.

## Combining with `--token`

The explicit `--token` flag and the `PARSEC_TOKEN` env var take
precedence over the persisted credential. Use this when an operator
needs to temporarily switch identities without `logout`-ing:

```bash
PARSEC_TOKEN="$break-glass-bearer" parsec keys retire <kid>
```
