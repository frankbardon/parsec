# Parsec

Parsec is a scalable realtime messaging engine built on
[Centrifuge](https://github.com/centrifugal/centrifuge) (OSS). It ships
as a Go library (primary), a CLI binary, and a Twirp JSON RPC service —
three surfaces over the same engine.

This book is the reference manual. Start with the
[quickstart](getting-started/quickstart.md), or skip to the section
that matches your role:

- **Building against parsec** — [library overview](library/overview.md)
  → [options](library/options.md) → [custom sinks](library/sinks.md)
- **Operating parsec** — [deployment](ops/deployment.md) →
  [config file](ops/config.md) → [observability](ops/observability.md)
- **Integrating from another language** — [Twirp RPC](rpc/twirp.md)
- **Curious about the model** — [architecture](internals/architecture.md)

## Capability map

| Need | Read |
|---|---|
| Try parsec in 30 seconds | [Running with Docker](getting-started/docker.md) |
| Wire a browser to parsec | [Browser client](getting-started/browser-client.md) |
| Test code that embeds parsec | [parsectest helpers](library/testing.md) |
| Open a channel, publish, subscribe | [Quickstart](getting-started/quickstart.md) |
| Understand the namespace grammar | [Channel naming](channels/naming.md) |
| Token-gate a channel with patterns | [ACL scopes](channels/acl.md) |
| Rotate signing keys without downtime | [Key rotation](ops/key-rotation.md) |
| Accept IdP-issued bearers | [OIDC bridge](ops/oidc.md) |
| Survive sink outages | [DLQ](ops/dlq.md) |
| Gate abusive callers | [Rate limiting](ops/rate-limiting.md) |
| Cluster across machines | [Deployment](ops/deployment.md) |
| Span regions | [Multi-region](ops/multi-region.md) |
| Speak HTTP/3 | [WebTransport](ops/webtransport.md) |
| Compress chatty channels | [Delta compression](ops/delta-compression.md) |
| Embed the operator UI | [Admin UI](ops/admin-ui.md) |
| Scrape metrics | [Observability](ops/observability.md) |

## What Parsec is

- A broker process that holds long-lived client connections
  (WebSocket + optional WebTransport).
- A channel manager with a strict `public:`/`private:` namespace
  convention and TTL-driven lifecycle.
- A library you embed in Go services to publish, subscribe, and
  authenticate.
- A CLI and a Twirp RPC surface — two ways to control the same engine
  from outside.

## What Parsec is not

- Not an everything-store. It does not own your business data; it
  ships events.
- Not a durable work queue. Use one for jobs; use parsec for
  "you have a notification".
- Not a Centrifugo Pro replacement. Parsec uses the OSS library; we
  ship our own delta-compression toggle, admin UI, and presence
  primitives — only the subset we need.
