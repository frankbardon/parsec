# Channel Naming Convention

Channel names are the contract between publishers and subscribers. Parsec
enforces a strict layout so every name announces its visibility, its owning
application, and the domain object it speaks for.

## Grammar

```
<visibility>:<app>.<domain>[.<id>][.<topic>]
```

- `visibility` — `public` or `private`. Required. Maps to a Centrifuge
  namespace at boot. Anything else is rejected at validation time.
- `app` — a short slug for the consuming application (e.g. `webapp`, `agent`,
  `cron`). Required. Lowercase ASCII letters, digits, and hyphens.
- `domain` — the root domain object (`session`, `user`, `download`,
  `analysis`, `alert`, `heartbeat`, `report`, `scoreboard`). Required.
- `id` — a stable identifier (UUID, session ID, user ID). Required for
  private; optional for public when the channel is broadcast-wide.
- `topic` — optional sub-topic for finer-grained subscription
  (`progress`, `complete`, `failed`).

Components are joined with `.` after the `:` separator. The `:` is reserved
for the visibility namespace and must not appear elsewhere.

## Examples

### Public

| Channel | Meaning |
|---|---|
| `public:webapp.system.status` | System-wide status feed for the webapp. |
| `public:scoreboard.counter.ai_users` | Counter that anyone in the org can subscribe to. |
| `public:agent.broadcast.maintenance` | Maintenance announcements from the agent runtime. |

### Private

| Channel | Meaning |
|---|---|
| `private:webapp.session.{sid}.notifications` | Per-session notification stream. |
| `private:webapp.user.{uid}.downloads` | Per-user async-download progress. |
| `private:agent.analysis.{job}.progress` | Per-job analysis progress. |

## Why this matters

- A grep for `public:` finds everything you can subscribe to without auth.
- A grep for `private:` finds everything that needs a token.
- The `app.domain` prefix tells you who owns it — kill that namespace and you
  cleanly remove a consumer.
- The `id` segment makes per-tenant fanout cheap: subscribers do not need
  server-side filters.

## Domain hygiene

Domain names are project-specific, but the foundation is fixed:

- Keep `domain` to a small vocabulary per `app`. If you have more than ~12
  domains, you probably want a new app slug.
- Treat `domain` plurality consistently. We use the singular form
  (`session`, not `sessions`).
- Reserve a `system` domain in every app for service-level events.

The full enforcement lives in `channels/name.go` and is exercised by the
CLI and the Twirp surface alike.

## Patterns

The same grammar — with two extra wildcards — describes the patterns
that may appear in a token's `scopes` claim:

- `*` matches exactly one segment, in any position.
- `**` matches one-or-more trailing segments; last position only.

Patterns are bound to a concrete visibility (`public` or `private`);
cross-visibility patterns like `*:foo.bar` are rejected at parse time.
See [Access Control Scopes](acl.md) for how scope patterns interact
with the `chs` exact-match list and the verb permissions.
