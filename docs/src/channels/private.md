# Private channels

Private channels are token-gated, time-boxed feeds: a subscriber must
present a valid access token tied to the channel name and subject.

```bash
./bin/parsec channels create private:webapp.user.42.downloads \
  --subject user-42 --ttl 30m
# {
#   "code": "OK",
#   "payload": {
#     "name": "private:webapp.user.42.downloads",
#     "access_token":  "eyJ...",
#     "refresh_token": "eyJ...",
#     "access_expires_unix":  1747935432,
#     "refresh_expires_unix": 1747937232
#   }
# }
```

The grammar (`channels.ParseName`) requires private channels to carry
an id segment; `private:webapp.user.downloads` is invalid because there
is no per-tenant scope. See [naming](naming.md) for the full rule set.

## What `create` returns

`channels create` mints a brand-new channel record AND issues the token
pair in one round trip. The response carries:

- `access_token` — short-lived (default 5 minutes; bounded by
  `parsec.Options.AccessTokenTTL`).
- `refresh_token` — lives up to the channel TTL, capped at 1 hour. Use
  it to mint fresh access tokens until the channel expires.
- `access_expires_unix` / `refresh_expires_unix` — absolute expiry
  timestamps.

The server does NOT keep the tokens. Lose them and you must recreate
the channel. This is intentional: Parsec stores channel records, not
secret material.

## TTL cap

The hard ceiling is one hour. The validator that enforces it is in
`channels/manager.go`:

```go
const MaxPrivateTTL = 1 * time.Hour
```

Requesting more returns `PARSEC_CHANNEL_TTL_EXCEEDS_MAX`. Most
deployments use 5–30 minutes; the upper bound is meant for long-running
download or analysis jobs, not for ambient session channels.

## Lifecycle

Private channels move through:

| State | Meaning | Effect on subscribers |
|---|---|---|
| `open` | Active. Accepts subscribes with a valid access token. | Receive publications. |
| `deleted` | Inactivity TTL exceeded OR explicit delete. | Unsubscribed. |

Note the absence of a `closed` state. Private channels do not "fade
out" — when their TTL elapses they are reaped from the manager
entirely. The reason: a closed-but-not-deleted private channel would
leak the channel's existence to anyone who asked, defeating the
visibility prefix.

## Refresh flow

Once an access token expires, the client exchanges its refresh token
for a new access:

```bash
./bin/parsec tokens refresh "<refresh-token>"
```

`RefreshToken` is a public RPC — no bearer required — because it
validates the refresh token in the body. The new access cannot outlive
the refresh. When the refresh expires the channel is effectively
unreachable; the next sweep reaps it.

## Touch and Publish

Every successful `Publish` calls `Manager.Touch` under the hood, which
resets `LastActive`. A heavily-used channel stays alive past its TTL by
the publishing client merely doing its job. An idle channel ages out
on its own.

If a client wants to keep a channel alive without sending data, the
library exposes `Manager.Touch(name)` directly. The CLI does not — there
is no operator use case.

## Typical use

- Per-user notifications: `private:webapp.user.<uid>.<topic>`
- Per-job progress: `private:agent.analysis.<job>.progress`
- Per-run failure streams: `private:tools.run.<run_id>.failures`

See also [public channels](public.md) and [TTL and expiry](ttl.md) for
how the lifecycle differs.

## Pattern-based grants

Tokens can carry a `scopes` claim — a set of channel-name pattern
grants with per-verb permissions. A single token can authorize a whole
namespace prefix like `private:webapp.user.42.*` instead of naming
every channel individually. See [Access Control Scopes](acl.md) for the
full grammar and examples.
