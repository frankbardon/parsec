# Browser end-to-end demo

The shortest path from a fresh clone to "I see realtime data in a
browser." One Go file, one HTML file, no Redis, no token plumbing.

## Run

```bash
go run ./examples/browser
# browser demo: open http://localhost:8000
```

Then open <http://localhost:8000> in any modern browser. The page
subscribes to `public:demo.system.heartbeat` via `centrifuge-js` and
renders the JSON heartbeat the Go server publishes every second.

`Ctrl-C` shuts the server down cleanly.

## What it shows

- **Server side**: `parsec.New` → `Run` in a goroutine → `OpenPublic`
  → a 1 Hz `Publish` loop. The same `Broker().Node()` powers
  `centrifuge.NewWebsocketHandler` mounted at `/connection/websocket`.
- **Client side**: vanilla `Centrifuge("ws://localhost:8000/connection/websocket")`,
  one `newSubscription(channel)`, one `on("publication", ...)` callback
  that prepends incoming rows to a `<ul>`.

The channel is public — no token is required to subscribe. For the
private-channel flow, see [`docs/src/getting-started/browser-client.md`](../../docs/src/getting-started/browser-client.md).

## File map

| File | Purpose |
|---|---|
| `main.go` | Go server that boots parsec, embeds the HTML page, and publishes a heartbeat |
| `index.html` | Single-file browser client using `centrifuge-js` over a CDN |

## Things to try

- Open the page in two browser tabs. Both subscriptions receive every
  publication — fan-out is the broker's default behaviour.
- `kill -STOP` the server; reload the page. Watch the status pill flip
  to "disconnected" with the reason from the centrifuge client.
- Resume with `kill -CONT`; the client auto-reconnects and resumes the
  feed.

For a private-channel walkthrough (token mint → browser presents JWT →
subscribe authorization), read the documentation page linked above.
