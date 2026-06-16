# parsec playground

A single-binary, in-memory developer playground for poking at every
parsec surface from a browser. **Not a deployment artifact.** Bearer
auth is disabled, the keyring is freshly bootstrapped on every boot,
and state dies with the process.

## Run

```bash
make playground
# or
go run ./examples/playground
```

Then open <http://localhost:8000/playground>.

The startup banner prints the URL, a bootstrap mgmt token (for
inspection — auth is off so it is decorative), and a couple of sample
channel names.

## What it does

The binary boots `parsec.New(...)` with an ephemeral `StateDir`,
registers two demo sinks (`memsink` always succeeds and captures
payloads; `flakysink` always fails transiently so messages land in the
DLQ), and applies a tight publish rate limit (5 per 10s per subject)
so the Rate-limit tab can trip the bucket in a few clicks.

It then mounts the full parsec HTTP surface (Twirp at
`/twirp/parsec.ParsecService/…`, WebSocket at `/connection/websocket`,
`/manifest`, `/healthz`, `/metrics`) **with bearer enforcement off**,
and adds a small `/playground/*` API for the things Twirp doesn't
expose:

- `GET /playground/bootstrap.json` — mgmt token + base URLs
- `GET /playground/sinks/memsink` — capture ring buffer
- `POST /playground/issue-access` — `Issuer.IssueAccess` wrapper
- `POST /playground/publish-or-sink` — `Parsec.PublishOrSink` wrapper
- `POST /playground/check-ratelimit` — `Parsec.CheckRateLimit` wrapper

The UI is a single `<script>` tag of AlpineJS over DaisyUI+Tailwind
served from an embedded `index.html`. No bundler, no build step.

## Tabs

| Tab | What to try |
|---|---|
| Publish | Open `public:demo.heartbeat`, send `{"hello":1}` |
| Subscribe | Connect to the same channel, watch the payload arrive |
| Tokens | Issue an access token for `public:demo.heartbeat` with a `public:demo.*` scope and `subscribe` verb; expand the decoded claims |
| Sinks | Publish-or-sink to `public:demo.notify` (no subscriber) via `memsink` then via `flakysink` |
| DLQ | List with sink filter `flakysink`; replay or discard the failed entry |
| Rate-limit | Hammer 12 publishes; watch the 6th return a `PARSEC_RATELIMIT_*` code |
| Manifest | Fetch `/manifest` and inspect what the instance advertises |
| Channels | Refresh the open channel list after publishing |

## Why not the CLI?

The CLI is great for one-shot operator work (`parsec publish`,
`parsec dlq replay`). The playground is for the other shape — sit on
one page, click around, see how the surfaces relate.

## Caveats

- In-memory only. Restart loses everything. Use docker-compose for the
  Redis-backed shape.
- Auth is off. Do not expose port 8000 beyond localhost.
- The flakysink retry budget is the parsec default; replay attempts
  also fail by design, so a DLQ entry will reappear after every
  replay until you discard it.
