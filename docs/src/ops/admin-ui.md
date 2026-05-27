# Admin UI

Parsec ships an embedded single-page application at `/admin` for
operator-grade diagnosis. This is **not** an end-user surface. The UI
is intentionally minimal — vanilla HTML, JavaScript and CSS, no
build pipeline, no frameworks, served from `embed.FS`.

> The admin UI is **off by default**. Operators opt in per deployment.
> When disabled the entire `/admin/*` tree returns 404, so no bytes
> from the asset bundle reach the response graph.

## Enabling

YAML:

```yaml
server:
  admin_ui:
    enabled: true
```

CLI:

```bash
parsec serve --admin-ui
```

Env:

```bash
PARSEC_ADMIN_UI=true parsec serve
```

The CLI flag overrides the YAML setting when explicitly passed.

## Confirming it's on

Hit `/manifest`; look for `admin_ui_enabled: true`:

```bash
curl -s http://localhost:8000/manifest | jq '.payload.admin_ui_enabled'
```

## Pages

| Path                  | Purpose                                              |
|-----------------------|------------------------------------------------------|
| `/admin/`             | Landing page + raw manifest dump                     |
| `/admin/login.html`   | Operator pastes a mgmt bearer; stored in `sessionStorage` |
| `/admin/channels.html`| List channels; open public; create private; delete  |
| `/admin/keys.html`    | List keys; generate; promote; retire                 |
| `/admin/dlq.html`     | Sink-scoped DLQ inspection; replay or discard items  |
| `/admin/limits.html`  | Read-only view of configured rate-limit budgets      |

## Auth model

The UI is a thin wrapper over the Twirp surface. Every action invokes
the same `POST /twirp/parsec.ParsecService/<Method>` route a `curl`
operator would call, with the mgmt bearer attached as
`Authorization: Bearer <token>`. The bearer lives in `sessionStorage`
which the browser clears when the tab closes — so leaving the UI
open is identical to leaving a shell with `PARSEC_TOKEN` exported.

The login form itself is plain static HTML; the Twirp calls enforce
the bearer, not the asset routes. A 401 from any RPC redirects the
page back to `login.html` and clears the stored token.

## Layout sketch

```
+--------------------------------------------------------------+
| parsec admin                                                 |
+--------------------------------------------------------------+
| Channels  Keys  DLQ  Limits                       sign out   |
+--------------------------------------------------------------+
| Operator-grade diagnostic UI. Pick a section above.          |
|                                                              |
| {                                                            |
|   "format_version": "1.0",                                   |
|   "kind": "parsec.manifest",                                 |
|   "payload": {                                               |
|     "service": "parsec",                                     |
|     "active_key_id": "k-...",                                |
|     "admin_ui_enabled": true,                                |
|     ...                                                      |
|   }                                                          |
| }                                                            |
+--------------------------------------------------------------+
```

## Limits

- No WebSocket subscriber view — the UI uses the Twirp surface only.
- No multi-user roles inside the UI. The mgmt bearer IS the role.
  Want different views for different operators? Mint mgmt tokens with
  pattern scopes and let the bearer's grants decide what each call
  succeeds at.
- No SSO from the UI. Use the OIDC bridge to obtain a mgmt bearer
  outside the UI and paste it in.
- No history. Refresh-survival of state is not a goal — the UI is a
  diagnostic, not a console.

## When to use it

- A pager fires; you want to see channel state without `curl + jq`.
- You're rotating keys and want to confirm the new key landed.
- You suspect a sink is backed up; you want a quick DLQ peek.
- You're showing parsec to a colleague and screenshots beat shell.

When **not** to use it:

- Building a UI for application users. Use the websocket transport
  + the issued credentials. The admin UI is operator-only.
- Automating any workflow. Use the CLI or RPC directly.
- Production deployments that don't want the bytes on the wire.
  Leave `server.admin_ui.enabled` at its default `false`.
