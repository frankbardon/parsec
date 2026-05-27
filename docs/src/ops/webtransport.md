# WebTransport (HTTP/3)

Parsec ships an optional HTTP/3 WebTransport listener alongside the
default WebSocket transport. Both transports terminate at the same
`centrifuge.Node`, so auth, channel state, and subscribe authorization
behave identically — only the wire layer differs.

Use WebTransport when you want:

- Lower head-of-line blocking under packet loss (QUIC streams vs. one
  TCP connection).
- Faster reconnects across mobile network changes (connection migration).
- A drop-in path for browsers on networks that block long-lived WebSocket
  upgrades.

WebTransport requires TLS — there is no plaintext mode. Browsers refuse
to upgrade an HTTP/3 transport without a valid certificate (or a known
SPKI hash for local development).

## Enabling the listener

### Library

```go
p, err := parsec.New(parsec.Options{
    StateDir: "/var/lib/parsec",
    WebTransport: parsec.WebTransportOptions{
        Addr:        ":8443",                              // UDP listen
        TLSCertFile: "/etc/parsec/tls/server.crt",
        TLSKeyFile:  "/etc/parsec/tls/server.key",
        AllowedOrigins: []string{
            "https://app.example.com",
        },
    },
})
```

`AllowedOrigins` is the WT-level CORS gate. An empty slice means "allow
all" — appropriate for local dev only.

### YAML

```yaml
server:
  addr: ":8000"
  webtransport:
    addr: ":8443"
    tls_cert_file: /etc/parsec/tls/server.crt
    tls_key_file: /etc/parsec/tls/server.key
    allowed_origins:
      - https://app.example.com
```

### CLI

```bash
parsec serve \
  --addr :8000 \
  --webtransport-addr :8443 \
  --webtransport-cert /etc/parsec/tls/server.crt \
  --webtransport-key  /etc/parsec/tls/server.key
```

The Twirp listener stays on `:8000`. The WT listener binds UDP on
`:8443`. Both share the same `Parsec` instance.

## Path and protocol

WT clients connect to `https://<host>:<wt-port>/connection/webtransport`.
The handler upgrades the request to a WebTransport session and accepts a
single bidirectional stream that carries length-prefixed framed
centrifuge protocol messages — the same payload shape used over
WebSocket.

The manifest exposes the available transports:

```json
{
  "transports": ["websocket", "webtransport"]
}
```

Browser clients that detect `webtransport` in the manifest can prefer
HTTP/3 and fall back to WebSocket on unsupported networks.

## Operating notes

- WebTransport listens on **UDP** — open the firewall for the chosen
  port (default UDP 8443) in addition to the TCP port serving Twirp /
  WebSocket.
- Some L7 load balancers do not yet proxy HTTP/3. Terminate TLS at the
  parsec process (or at a QUIC-aware proxy) until your edge supports it.
- Certificate rotation is identical to any HTTP/3 service: write a new
  cert pair and restart the process. There is no hot-reload yet.
- The WT transport uses the **same** access tokens as WebSocket. Clients
  open a bidi stream with the access token as the first frame; subscribe
  authorization runs through `auth.SubscribeAuthorizer`, so deny-wins
  scope evaluation behaves identically.

## Observability

Per-transport counters are emitted on the standard collectors:

```
parsec_transport_connections_total{transport="webtransport", result="ok"}
parsec_transport_connections_total{transport="webtransport", result="rejected"}
```

Access logs include `transport=webtransport` for the upgrade request. The
underlying QUIC handshake is not logged — operators wanting that detail
should enable quic-go's tracer in a future release.

## Disabling

Omit `WebTransportOptions.Addr` (or the YAML `server.webtransport`
block) and the entire HTTP/3 stack is excluded — no UDP listener, no
extra goroutines. The manifest then advertises `"transports":
["websocket"]` only.

## Limitations

- No automatic certificate rotation (process restart required).
- Centrifuge OSS does not ship a WebTransport handler upstream;
  parsec implements `wtTransport` directly against `centrifuge.Transport`.
  Behavior matches the WebSocket transport but the implementation is
  parsec-local — file issues here, not against the upstream project.
- `quic-go` requires Go 1.22+ (parsec targets 1.26.1) and ALSO requires
  a kernel with sufficient UDP socket buffer space on Linux — the
  `net.core.rmem_max` sysctl may need bumping for high-throughput
  deployments.
