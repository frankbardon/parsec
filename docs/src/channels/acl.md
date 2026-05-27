# Access Control Scopes

Parsec tokens carry two grant sources:

1. **`chs`** — an exact-match list of channel names. A token whose
   `chs` contains `private:webapp.user.42.profile` can subscribe to
   that channel and nothing else.
2. **`scopes`** — a set of channel-name pattern grants. Each scope
   binds a pattern to a list of verbs (`subscribe`, `publish`,
   `manage`).

A token may carry one or both. The check at subscribe/publish time is
the union: any path that authorizes is sufficient.

## Why patterns

Pure `chs` requires the operator to know every channel a token may
touch at issuance time. For multi-tenant SaaS workloads where a user
owns a namespace prefix and channels are created on the fly inside that
prefix, this means either pre-minting hundreds of tokens or
re-issuing constantly.

Scopes let a single token say "this user owns the prefix
`private:webapp.user.42.`" and survive every new channel created under
that prefix until the token expires.

## Grammar

A scope pattern is a channel name with two wildcard primitives layered
on top:

```
<visibility>:<seg>(.<seg>)*[.*][.**]
```

- `<visibility>` must be `public` or `private`. Cross-visibility
  patterns (`*:foo.bar`) are rejected at issuance with
  `PARSEC_INVALID_ARGUMENT`.
- `<seg>` is a literal channel-name segment (lowercase ASCII, digits,
  `-`, `_`).
- `*` matches exactly one segment, in any position.
- `**` matches one-or-more trailing segments; it is only legal as the
  last segment of the pattern.
- The total segment count is capped at 8 — pathological tokens with
  `**.**.**...` are rejected.

### Truth table

| Pattern | Channel | Matches? |
|---|---|---|
| `private:foo.bar.7` | `private:foo.bar.7` | yes |
| `private:foo.bar.7` | `private:foo.bar.8` | no |
| `private:foo.bar.*` | `private:foo.bar.7` | yes |
| `private:foo.bar.*` | `private:foo.bar.7.x` | no |
| `private:foo.bar.**` | `private:foo.bar.7` | yes |
| `private:foo.bar.**` | `private:foo.bar.7.x.y` | yes |
| `private:foo.*` | `public:foo.bar` | no |
| `*:foo.bar` | (any) | rejected at parse |

## Verbs

Three verbs are recognized:

| Verb | Where it applies |
|---|---|
| `subscribe` | the broker's subscribe authorizer. A `chs` exact-match entry authorizes every verb (including `subscribe`). |
| `publish` | client-side publish over the websocket. Server-side publishes via the management RPC remain gated by the mgmt bearer. |
| `manage` | per-channel management RPCs when called with an access token instead of a mgmt token (delegated administration). |

A scope must list at least one verb. The check at runtime requires the
exact verb the surface is asking for — a `subscribe`-only scope cannot
publish, even on a channel the pattern matches.

## Example token

A scope-bearing access token's payload looks like this:

```json
{
  "sub": "user-42",
  "typ": "access",
  "chs": ["private:webapp.user.42.profile"],
  "scopes": [
    {
      "pat": "private:webapp.user.42.**",
      "v": ["subscribe", "publish"]
    }
  ],
  "iat": 1700000000,
  "exp": 1700000300
}
```

This token can:

- subscribe to `private:webapp.user.42.profile` (Chs exact match — any verb)
- subscribe to `private:webapp.user.42.downloads` (scope match + verb)
- subscribe to `private:webapp.user.42.session.notifications` (`**` match)
- publish to `private:webapp.user.42.profile` (scope match + verb)

It **cannot** subscribe to `private:webapp.user.43.profile`
(scope does not match).

## chs-only tokens

A token with only `chs` and no `scopes` is the simplest shape: the
exact-match list is checked, and any name in the list authorizes every
verb. A token with both `chs` and `scopes` widens the grant — the
authorization check is the union.

## Deny scopes

A scope may set `deny: true` to *subtract* from the grant set. The
on-wire shape is identical to an allow scope plus the new field:

```json
{
  "pat": "private:webapp.user.42.financials",
  "v":   ["subscribe"],
  "deny": true
}
```

The `deny` key is omitted from the JSON when false, so allow-only
tokens stay compact on the wire.

### Precedence: deny wins

`Claims.Authorizes` evaluates a single (channel, verb) request in this
order:

1. **Deny pass.** Walk every scope where `deny: true`. If the pattern
   matches the channel AND the verb is in the scope's verb list,
   return *deny* immediately.
2. **`chs` pass.** Walk the `chs` exact-match list. Any entry equal to
   the channel returns *allow*.
3. **Allow pass.** Walk every scope where `deny: false`. If the pattern
   matches the channel AND the verb is in the verb list, return
   *allow*.
4. Otherwise return *deny*.

A matching deny *always* wins — it overrides both `chs` exact matches
and any overlapping allow scope. This mirrors AWS IAM, GCP IAM, and
similar systems: deny is non-overridable, so operators can carve a hole
in a permissive grant without having to enumerate every channel they
*do* want to allow.

A deny scope without any overlapping allow is a defensive no-op: the
channel already had no grant; the deny does not unlock anything.

The manifest exposes this with two fields:

```json
{
  "deny_supported":   true,
  "scope_precedence": "deny-wins"
}
```

### Worked example

The operator wants `user-42` to subscribe to anywhere under their user
prefix EXCEPT a sensitive financials channel.

```bash
parsec channels create private:webapp.user.42.profile \
  --subject user-42 \
  --scope "private:webapp.user.42.*" \
  --deny-scope "private:webapp.user.42.financials" \
  --verb subscribe
```

The minted access token's scope set is:

```json
[
  {"pat": "private:webapp.user.42.*", "v": ["subscribe"]},
  {"pat": "private:webapp.user.42.financials", "v": ["subscribe"], "deny": true}
]
```

At subscribe time:

| Channel | Allow matches? | Deny matches? | Result |
|---|---|---|---|
| `private:webapp.user.42.profile` | yes | no | allow |
| `private:webapp.user.42.downloads` | yes | no | allow |
| `private:webapp.user.42.financials` | yes | **yes** | **deny** |
| `private:webapp.user.43.profile` | no | no | deny (no allow) |

### CLI flags

`parsec channels create` accepts two new flags alongside `--scope` and
`--verb`:

- `--deny-scope <pattern>` (repeatable) — adds a deny scope.
- `--deny-verb <verb>` (repeatable) — verb list the deny scopes apply
  to. When omitted, the deny scopes inherit the `--verb` list (default
  `subscribe`).

### Security note: deny patterns are visible in the token

The deny scope rides in the access token in clear (signed but not
encrypted, like every other claim). An operator that mints a token
with `deny: private:tenant.x.secret-project` is telling the holder of
that token "secret-project exists." Do **not** treat deny patterns as
confidential — if the channel name itself is sensitive, the deny
belongs at the broker level (a server-side ACL Parsec does not yet
ship), not in the token.

The trade-off is intentional: keeping deny inside the token avoids a
round-trip per subscribe and keeps the authorization decision local to
the broker's connection.

## Issuance

Over the CLI:

```bash
parsec channels create private:webapp.user.42.profile \
  --subject user-42 \
  --scope "private:webapp.user.42.*" \
  --scope "private:webapp.user.42.session.*" \
  --verb subscribe \
  --verb publish
```

Each `--scope` adds a pattern; `--verb` flags apply to every scope on
the command. If no `--verb` is given, `subscribe` is the default.

Over the Twirp RPC (`CreatePrivate`):

```json
{
  "name": "private:webapp.user.42.profile",
  "subject": "user-42",
  "ttlSeconds": 900,
  "scopes": [
    {"pattern": "private:webapp.user.42.*", "verbs": ["subscribe"]}
  ]
}
```

Programmatic Go:

```go
creds, err := svc.CreatePrivateChannel(ctx, "user-42",
    "private:webapp.user.42.profile", 15*time.Minute,
    []auth.Scope{
        {Pattern: "private:webapp.user.42.**", Verbs: []auth.Verb{auth.VerbSubscribe}},
    })
```

## Performance

The pattern matcher is hand-rolled (no regex) and runs in well under
500ns per `Matches` call. Parsed patterns are cached on the Scope value
the first time the matcher consults them — subsequent calls reuse the
compiled segments without re-parsing. There are no string allocations
in the hot path.

When `scopes` is empty, `Claims.Authorizes` short-circuits to the
`chs` exact-match loop, so tokens without pattern grants pay zero
overhead for the pattern matcher.
