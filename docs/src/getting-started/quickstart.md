# Quick start

Boot the broker, open a public channel, publish, mint a private channel,
and exchange a refresh token. Five minutes end-to-end.

## 1. Install

```bash
go install github.com/frankbardon/parsec/cmd/parsec@latest
```

Or build from source:

```bash
git clone https://github.com/frankbardon/parsec.git
cd parsec
make build   # binary lands at bin/parsec
```

## 2. Boot the server

The simplest invocation listens on `:8000` with a persistent keyring
under `/var/lib/parsec`. First boot mints a bootstrap mgmt token and
prints it to stderr — capture it.

```bash
parsec serve --addr :8000 --state-dir /var/lib/parsec
# parsec serve: bootstrap mgmt token (expires 2026-05-23 18:04:11 UTC):
# eyJhbGciOiJIUzI1NiIs...
```

`--state-dir` makes the HMAC keyring file-backed so tokens survive
restart. Omit it for fully ephemeral dev runs.

See [serve](../cli/serve.md) for every flag.

## 3. Export your operator credentials

Most CLI commands hit the management RPC, so they need the bearer:

```bash
export PARSEC_TOKEN="eyJhbGciOiJIUzI1NiIs..."
export PARSEC_SERVER="http://localhost:8000"
```

Already use an IdP? Run [`parsec login oidc`](../cli/login.md) instead
of pasting a bearer.

## 4. Open a public channel

```bash
parsec channels open public:webapp.system.status
```

Public channels have no token gate — anyone can subscribe. They still
have a TTL (30 min inactivity by default; override with `--ttl 1h`).

## 5. Publish to it

```bash
echo '{"msg":"hello"}' | parsec publish public:webapp.system.status
```

The broker returns a publish offset and an epoch. Late subscribers can
replay from any offset within the 5-minute history window.

## 6. Subscribe as a probe

```bash
parsec subscribe public:webapp.system.status
```

Server-Sent Events client for debugging. Publications appear as NDJSON
on stdout; `Ctrl-C` to exit. Production clients use the WebSocket
(`/connection/websocket`) or the optional
[WebTransport](../ops/webtransport.md) endpoint.

## 7. Mint a private channel

Private channels need an access token. `channels create` returns the
access + refresh pair in one response:

```bash
parsec channels create private:webapp.user.42.downloads \
  --subject user-42 --ttl 30m
```

Access tokens default to 5 minutes; refresh tokens live up to the
channel TTL (capped at 1 hour). The token bodies are JWTs you can pass
to `centrifuge-js` directly. See
[private channels](../channels/private.md).

## 8. Exchange a refresh for a fresh access

```bash
parsec tokens refresh "<refresh-token>"
```

`RefreshToken` is public — the refresh token in the body authenticates
the call. The new access token cannot outlive the refresh expiry.

## Next steps

- **Production deployment** — [config file](../ops/config.md),
  [deployment](../ops/deployment.md).
- **Cluster across machines** — point `Options.RedisClient` at a shared
  Redis; the broker, registry, keyring, DLQ, and rate limiter all
  switch automatically.
- **Authenticate with patterns** — [ACL scopes](../channels/acl.md)
  cover `*` / `**` grants and deny-wins precedence.
- **Embed in a Go service** — [library overview](../library/overview.md).
- **Operate** — [key rotation](../ops/key-rotation.md), [DLQ](../ops/dlq.md),
  [observability](../ops/observability.md), [rate limiting](../ops/rate-limiting.md).
- **Troubleshoot** — [error codes catalog](../ops/troubleshooting.md).
