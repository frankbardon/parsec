# `parsec channels`

Manage channels on a remote Parsec server.

```bash
export PARSEC_TOKEN=$(cat ~/.parsec/mgmt-token)
export PARSEC_SERVER=https://parsec.example.com

./bin/parsec channels list
./bin/parsec channels open public:webapp.system.status --ttl 1h
./bin/parsec channels create private:webapp.user.42.downloads --subject user-42 --ttl 30m
./bin/parsec channels get  private:webapp.user.42.downloads
./bin/parsec channels delete public:webapp.system.status
```

Every subcommand routes to the Twirp service at
`/twirp/parsec.ParsecService/`. The CLI is a thin adapter — anything
you can do here is reachable over Twirp.

## Auth

The `--server` and `--token` flags (or `PARSEC_SERVER` /
`PARSEC_TOKEN`) are required. Tokens are mgmt bearers minted at boot
by `parsec serve` or rotated via the
[key-rotation runbook](../ops/key-rotation.md).

## list

Returns every channel the manager currently knows about, including
those in `closed` state. Deleted records do not appear.

```text
NAME:
   parsec channels list - List all managed channels

USAGE:
   parsec channels list [options]

OPTIONS:
   --help, -h  show help

GLOBAL OPTIONS:
   --json  Output the parsec manifest as a descriptor envelope
```

## open

Opens (or reopens) a public channel. Idempotent — calling it on an
existing open channel resets `LastActive` and the TTL.

```text
NAME:
   parsec channels open - Open (or re-open) a public channel

USAGE:
   parsec channels open [options] <channel-name>

OPTIONS:
   --ttl duration  Inactivity TTL before close (default: 30m0s)
   --help, -h      show help

GLOBAL OPTIONS:
   --json  Output the parsec manifest as a descriptor envelope
```

## create

Mints a private channel and returns its one-shot access + refresh
tokens. The TTL is hard-capped at 1 hour. See
[private channels](../channels/private.md) for the lifecycle.

```text
NAME:
   parsec channels create - Create a private channel and print its one-shot access + refresh tokens

USAGE:
   parsec channels create [options] <channel-name>

OPTIONS:
   --ttl duration               Inactivity TTL (max 1h) (default: 15m0s)
   --subject string, -s string  Subject (sub claim) embedded in the tokens; typically a user id
   --scope string (repeatable)  Channel-name pattern grant baked into the issued tokens (e.g. private:webapp.user.42.*)
   --verb string  (repeatable)  Verb granted on the listed scopes. Allowed: subscribe, publish, manage. Default: subscribe
   --help, -h                   show help

GLOBAL OPTIONS:
   --json  Output the parsec manifest as a descriptor envelope
```

### Pattern grants (--scope / --verb)

Beyond the exact-match `chs` claim, the issued tokens may carry a set
of pattern-based grants in the `scopes` claim. Each `--scope` adds a
channel-name pattern; `--verb` flags apply to every pattern on the
command. With no `--verb`, the default is `subscribe`.

```bash
./bin/parsec channels create private:webapp.user.42.profile \
  --subject user-42 \
  --scope "private:webapp.user.42.*" \
  --scope "private:webapp.user.42.session.*" \
  --verb subscribe --verb publish
```

See [Access Control Scopes](../channels/acl.md) for the grammar,
truth table, and verb semantics.

## get

Single-channel snapshot. Returns `PARSEC_CHANNEL_NOT_FOUND` if the
channel was never opened or has been deleted.

```text
NAME:
   parsec channels get - Show a single channel's metadata

USAGE:
   parsec channels get [options] <channel-name>

OPTIONS:
   --help, -h  show help

GLOBAL OPTIONS:
   --json  Output the parsec manifest as a descriptor envelope
```

## delete

Final. Removes the channel from the manager and unsubscribes every
connected client. Private channels normally auto-delete on TTL; you
rarely call `delete` for them. Public channels require an explicit
delete (or a server restart, since channel state is in-memory).

```text
NAME:
   parsec channels delete - Delete a channel (public channels must be deleted explicitly; private auto-expire)

USAGE:
   parsec channels delete [options] <channel-name>

OPTIONS:
   --help, -h  show help

GLOBAL OPTIONS:
   --json  Output the parsec manifest as a descriptor envelope
```

## Reference

```text
NAME:
   parsec channels - Manage channels on a remote parsec server

USAGE:
   parsec channels [command [command options]]

COMMANDS:
   list    List all managed channels
   open    Open (or re-open) a public channel
   create  Create a private channel and print its one-shot access + refresh tokens
   get     Show a single channel's metadata
   delete  Delete a channel (public channels must be deleted explicitly; private auto-expire)

OPTIONS:
   --server string  Parsec server base URL (default: "http://localhost:8000") [$PARSEC_SERVER]
   --token string   Mgmt bearer token for the management API [$PARSEC_TOKEN]
   --help, -h       show help
```

## See also

- [publish](publish.md) — send data once a channel exists.
- [Channel naming convention](../channels/naming.md).
- [Twirp service](../rpc/twirp.md) for the wire shape.
