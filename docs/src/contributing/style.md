# Style guide

How to write Go that fits the rest of the codebase.

```go
// Package channels owns the public/private channel namespace and the
// lifecycle manager. Every other Parsec surface defers to ParseName for
// validation.
package channels
```

Two reflexes summarize most of the rules: keep functions short, and
let the type system carry the contract.

## Files

- Short. Most files in the repo are under 200 lines. If a file is
  growing past 300, split it by responsibility.
- Package-doc on every package, on the file named for the package
  (`channels/name.go` carries the `// Package channels...` doc).
- One package per directory. No subpackages by hand.

## Comments

- Comment what is not obvious. Do not echo the function signature in
  English.
- Comment every exported symbol with a sentence that starts with its
  name, per `gofmt` convention.
- Do not narrate the implementation inside a function. If you feel the
  need to, the function is probably too long.

A bad comment:

```go
// Touch resets the LastActive timestamp on the channel.
func (m *Manager) Touch(name Name) error {
    // ...
}
```

A good comment:

```go
// Touch marks the channel as recently active so the tick loop will not
// expire it on the next sweep.
func (m *Manager) Touch(name Name) error {
    // ...
}
```

The first one is the signature in English; the second carries the
intent.

## Errors

- All errors that cross a package boundary are `*errors.Error` from
  `github.com/frankbardon/parsec/errors`. Never `errors.New(...)` from
  the stdlib at a boundary.
- Codes are `PARSEC_DOMAIN_CATEGORY` strings. Pick the right one from
  `errors/codes.go`; if none fits, add one in that file in the same PR
  and update the RPC mapping in `rpc/server.go:writeServiceError`.
- Wrap with `errors.Wrap(code, msg, cause)` when you have an
  underlying error; otherwise `errors.New(code, msg)`.

Internal helpers can use any error type — but the moment the error is
returned from an exported function, it must be coded.

## Channel names

There is exactly one validator: `channels.ParseName`. Every surface
calls it. Do not reimplement parsing logic anywhere else. If you need
a new visibility prefix or a new component rule, the change lands in
`channels/name.go` and gains tests in `channels/name_test.go`.

## Library-first

`parsec.Parsec` is the deliverable. Everything else (CLI, RPC) is a
translator.

- `cmd/parsec/main.go` parses flags. Nothing else.
- `internal/cli/*` builds command structures. The bodies dispatch to
  `service.Service` or `*parsec.Parsec`. No business logic.
- If a CLI subcommand grows logic that the library cannot also call,
  the CLI is wrong. Pull the logic into a library package and let the
  CLI re-use it.

## Filesystem access

Library code uses `afero.Fs` (`opts.FS`). Never `os.Open` /
`os.ReadFile` inside library packages. The CLI is the one allowed
exception (e.g. `publish --file`).

## Descriptor envelopes

Every JSON output (CLI `--json`, `/manifest`) wraps its payload in
`descriptor.Envelope`. The format version bumps on any wire-shape
change.

## Tests

- Table-driven where it helps. `channels/name_test.go` is the model.
- Inject the clock for any time-dependent test. `Manager.SetClock` is
  there for this; do not call `time.Sleep`.
- Tests live next to the code, in `_test.go` files. The naming pattern
  is `TestThing_subcase`. Avoid `TestX` followed by 30 unrelated
  scenarios.

## Imports

- Group: stdlib, third-party, project-local. `gofmt` enforces.
- No dot imports.
- No blank imports outside `main` packages.

## Naming

- Package names are lower-case, one word.
- Exported types are `TitleCase`. Exported constants are also
  `TitleCase` (no `MAX_FOO` shouting).
- Receivers are short — a single letter that matches the type
  (`m *Manager`, `b *Broker`, `p *Parsec`). The receiver name is
  consistent across every method on the type.

## Anti-patterns to refuse

These show up in PRs and should be pushed back on:

- A new `internal/` package that duplicates `service/` logic.
- A CLI command that calls `broker.*` or `channels.*` directly. Go
  through `service.Service`.
- A new channel-validation helper outside `channels/`.
- A change to a public surface (CLI, RPC, library) without an update
  to the corresponding docs page in the same PR.

## See also

- [Development setup](setup.md).
- The `CLAUDE.md` at the repo root — the operational contract this
  guide derives from.
