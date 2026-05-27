# parsec subscribe

Tail a parsec channel from the terminal. Implemented as a
Server-Sent Events (SSE) client against the server's `/sse?channel=<name>`
endpoint. Intended as a **debug probe**, not a production transport —
production clients use the WebSocket (`/connection/websocket`) or
WebTransport (`/connection/webtransport`) endpoints.

## Usage

```bash
parsec subscribe <channel-name>
```

Each publication appears on stdout as one NDJSON envelope:

```json
{"channel":"public:webapp.system.status","offset":42,"data":{"msg":"hello"}}
```

`Ctrl-C` exits cleanly.

## Flags

| Flag | Env | Default |
|---|---|---|
| `--server` | `PARSEC_SERVER` | `http://localhost:8000` |
| `--token`  | `PARSEC_TOKEN`  | (empty) |
| `--from-offset` | — | `0` (latest) |

For private channels, set `--token` to an access token (NOT a mgmt
bearer):

```bash
parsec subscribe private:webapp.user.42.downloads \
  --token "$(parsec channels create private:webapp.user.42.downloads --subject u42 | jq -r .payload.access_token)"
```

## Why not for production?

SSE polls. The server holds a long-lived HTTP connection but the
backpressure model and reconnection semantics are simpler than the
WebSocket transport — fine for a debug probe, suboptimal for browser
clients. Use `centrifuge-js` (or the parsec library directly inside a
Go service) for real workloads.
