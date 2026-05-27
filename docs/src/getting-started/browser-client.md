# Browser client

This page walks through the full browser ↔ parsec round-trip:

1. A Go server hosts parsec and an HTML page.
2. The browser connects to the websocket transport using
   [`centrifuge-js`](https://github.com/centrifugal/centrifuge-js).
3. Public channels need no token; private channels present a JWT
   access token at connect time.

Parsec speaks the Centrifugo wire protocol unchanged, so the official
JS client is a drop-in. No custom SDK ships in this repo — every
example below uses `centrifuge-js` directly.

## 1. Public channel — no auth

The shortest demo. Boot a parsec instance, open a public channel,
mount the websocket handler, serve an HTML page.

```go
// examples/browser/main.go (excerpt)
p, _ := parsec.New(parsec.Options{StateDir: dir})
go p.Run(ctx)
for !p.Broker().Started() { time.Sleep(5*time.Millisecond) }

p.OpenPublic("public:demo.system.heartbeat", time.Hour)

mux := http.NewServeMux()
mux.Handle("/connection/websocket",
    centrifuge.NewWebsocketHandler(p.Broker().Node(), centrifuge.WebsocketConfig{}))
mux.Handle("/", http.FileServer(http.Dir("./public")))
http.ListenAndServe(":8000", mux)
```

The browser side is ~15 lines:

```html
<script src="https://unpkg.com/centrifuge@5.4.0/dist/centrifuge.js"></script>
<script>
  const c = new Centrifuge("ws://" + location.host + "/connection/websocket");
  c.on("connected", () => console.log("connected"));

  const sub = c.newSubscription("public:demo.system.heartbeat");
  sub.on("publication", (ctx) => console.log("got:", ctx.data));
  sub.subscribe();

  c.connect();
</script>
```

Run the bundled example to see it live:

```bash
go run ./examples/browser
# open http://localhost:8000
```

See [`examples/browser/`](https://github.com/frankbardon/parsec/tree/main/examples/browser)
for the full source.

## 2. Private channel — token-gated

Private channels require an access token. The flow:

1. **Server mints the token** via `parsec.CreatePrivate` (or the Twirp
   `CreatePrivate` RPC).
2. **Server hands the token to the browser** — typically as part of a
   session-bootstrap response or a dedicated `GET /token` endpoint
   gated by your existing app session.
3. **Browser presents the token to centrifuge** via `setToken(...)`
   before connect.
4. **Parsec verifies + authorizes subscribe** against the access
   token's `chs` list and pattern scopes.

### Server (Go)

```go
// mintToken returns the access token for the current user.
func mintToken(p *parsec.Parsec, userID string) (string, error) {
    creds, err := p.CreatePrivate(userID,
        fmt.Sprintf("private:webapp.user.%s.downloads", userID),
        30*time.Minute)
    if err != nil {
        return "", err
    }
    return creds.AccessToken, nil
}

// HTTP handler: returns { token, channel } as JSON. Your app's session
// guard runs first — only authenticated users get a token.
func tokenHandler(p *parsec.Parsec) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        uid := sessionUserID(r) // your auth glue
        if uid == "" {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        tok, err := mintToken(p, uid)
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        json.NewEncoder(w).Encode(map[string]string{
            "token":   tok,
            "channel": fmt.Sprintf("private:webapp.user.%s.downloads", uid),
        })
    }
}
```

Mount it alongside the websocket handler:

```go
mux.HandleFunc("/token", tokenHandler(p))
mux.Handle("/connection/websocket",
    centrifuge.NewWebsocketHandler(p.Broker().Node(), centrifuge.WebsocketConfig{}))
```

### Browser

```html
<script src="https://unpkg.com/centrifuge@5.4.0/dist/centrifuge.js"></script>
<script>
  async function main() {
    // Your app's auth cookie carries the user identity into /token.
    const res = await fetch("/token", { credentials: "include" });
    if (!res.ok) throw new Error("token mint failed");
    const { token, channel } = await res.json();

    const c = new Centrifuge("ws://" + location.host + "/connection/websocket", {
      token,
    });

    const sub = c.newSubscription(channel);
    sub.on("publication", (ctx) => console.log("got:", ctx.data));
    sub.on("error", (ctx) => console.error("sub error:", ctx));
    sub.subscribe();

    c.connect();
  }
  main().catch(console.error);
</script>
```

### Refresh — keeping the connection alive past 5 minutes

Access tokens are short-lived (5 min default). When the server says the
token is about to expire, centrifuge-js fires a `refresh` event — the
client exchanges the **refresh token** for a fresh access token via
parsec's public `RefreshToken` RPC:

```js
const c = new Centrifuge("ws://" + location.host + "/connection/websocket", {
  token,
  getToken: async (ctx) => {
    // Exchange the refresh token (held server-side) for a new access
    // token. The /refresh endpoint is your shim around the parsec
    // RefreshToken RPC.
    const res = await fetch("/refresh", { credentials: "include" });
    const { token } = await res.json();
    return token;
  },
});
```

On the server, `/refresh` calls `p.RefreshAccess(refreshToken)`:

```go
func refreshHandler(p *parsec.Parsec) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        refreshTok := sessionRefreshToken(r) // your storage
        out, err := p.RefreshAccess(refreshTok)
        if err != nil {
            http.Error(w, err.Error(), http.StatusUnauthorized)
            return
        }
        json.NewEncoder(w).Encode(map[string]string{"token": out.AccessToken})
    }
}
```

The refresh token can be stored in an HttpOnly cookie or a server-side
session — never expose it to JavaScript directly.

## 3. WebTransport (HTTP/3)

If [WebTransport](../ops/webtransport.md) is enabled, the centrifuge-js
options take a `webtransport: true` toggle:

```js
new Centrifuge([
  { transport: "webtransport", endpoint: "https://" + location.host + ":8443/connection/webtransport" },
  { transport: "websocket",    endpoint: "ws://" + location.host + "/connection/websocket" },
])
```

The client tries WebTransport first; on failure (network restrictions,
TLS issues, older browsers) it falls back to WebSocket.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Connect succeeds, subscribe rejects with `permission denied` | Access token missing `chs` for the channel, or its scopes deny the verb | Mint via `CreatePrivate` for that channel name; check scope grants. |
| Centrifuge logs "bad request" on connect | Token expired between mint and use | Shorten the mint→connect window; use `getToken` for live refresh. |
| Page loads, websocket immediately closes with `transport: token is invalid` | Server rotated keys; cached token signed by retired key | Re-mint the token; check `parsec keys list`. |
| 404 on `/connection/websocket` | Handler not mounted | `mux.Handle("/connection/websocket", centrifuge.NewWebsocketHandler(...))` |

For deeper protocol questions, see the upstream centrifuge-js
[docs](https://centrifugal.dev/docs/transports/client_protocol). The
parsec server speaks that protocol verbatim — anything that works
against Centrifugo works against parsec.
