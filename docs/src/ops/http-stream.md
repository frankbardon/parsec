# HTTP-Streaming Transport

Parsec mounts a bidirectional HTTP-streaming endpoint at
`/connection/http_stream` alongside the WebSocket transport. Both
transports terminate at the same `centrifuge.Node`, so auth, channel
state, and subscribe authorization behave identically — only the wire
layer differs.

Use HTTP-streaming when:

- A client network blocks WebSocket upgrades (corporate proxies,
  certain mobile carriers) but still allows long-lived HTTP/1.1 or
  HTTP/2 POST responses.
- You want an emulation transport for environments where WebSocket
  libraries aren't available (e.g. minimal HTTP-only clients,
  serverless runtimes).
- You need a parallel fallback to WebSocket without standing up
  HTTP/3 + TLS for WebTransport.

> **Not the same as `/sse`.** The `/sse` endpoint is a tiny
> polling-backed Server-Sent Events probe used by `parsec subscribe`
> — it is not a production transport. `/connection/http_stream` is a
> full bidirectional centrifuge transport that production SDKs use.

## Wire format

| Direction | Shape |
|---|---|
| Client → server | A single `POST` body carrying the connect command (newline-delimited JSON, or octet-protobuf when `Content-Type: application/octet-stream`). |
| Server → client | Streaming response body; newline-delimited JSON frames (or framed protobuf for the binary protocol) until the client disconnects or the server closes. |

The handler sets the standard streaming response headers so reverse
proxies don't buffer:

```
Cache-Control: private, no-cache, no-store, must-revalidate, max-age=0
X-Accel-Buffering: no
Pragma: no-cache
Expire: 0
```

CORS preflight (`OPTIONS`) returns `204 No Content` with
`Access-Control-Allow-Methods: POST, OPTIONS` so browser clients can
POST cross-origin.

## Connecting from `centrifuge-js`

```js
import { Centrifuge } from 'centrifuge';

const client = new Centrifuge({
  transports: [
    { transport: 'websocket', endpoint: 'wss://parsec.example.com/connection/websocket' },
    { transport: 'http_stream', endpoint: 'https://parsec.example.com/connection/http_stream' },
  ],
  // Token comes from your auth flow — see docs/src/library/options.md.
  token: accessToken,
});

client.on('connected', (ctx) => console.log('transport:', ctx.transport));
client.connect();
```

The SDK falls back through the `transports` list in order: WebSocket
first, then HTTP-streaming when the upgrade is refused.

## Manifest exposure

The `transports` field of the public Manifest advertises
`http_stream` so clients can discover availability:

```json
{
  "kind": "parsec.manifest",
  "format_version": "1",
  "payload": {
    "transports": ["websocket", "http_stream"]
  }
}
```

When `WebTransport.Addr` is also set, the list becomes
`["websocket", "http_stream", "webtransport"]`.

## Tuning

The handler accepts a `centrifuge.HTTPStreamConfig` (see the
`centrifuge` module). Parsec uses the zero value today — defaults are
fine for almost every deployment:

- `MaxRequestBodySize` defaults to 64 KiB.
- `PingPongConfig` defaults to the node-wide ping/pong settings.

If you need to tighten the request-body cap, open an issue — we'll
plumb the knob through `parsec.Options`.

## Observability

HTTP-streaming connections appear in:

- `/metrics` — the existing centrifuge collectors (`centrifuge_num_clients`,
  `centrifuge_transport_messages_*`) count http-stream sessions the
  same way they count WebSocket sessions.
- Access log — every POST to `/connection/http_stream` lands one
  structured INFO line with `method=POST`, `path=/connection/http_stream`,
  `duration_ms` (the full stream lifetime), `bearer_subject` (when the
  connect command carried a token).
