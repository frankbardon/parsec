# CLAUDE.md — Parsec

This file is loaded by Claude Code on every session start. It defines
the process and policy for working in Parsec. Implementation contracts
are split into on-demand context files (see the index below).

Parsec is a realtime messaging engine on top of the
[Centrifuge](https://github.com/centrifugal/centrifuge) OSS library. It
ships as a Go library (primary), a CLI (thin adapter), and a
Twirp-JSON RPC server (full external control). The library is the
deliverable. Every other surface is a translator.

## Standard workflow for new features and fixes

When the user asks to add a feature, fix a bug, or implement a change,
follow the playbook in [`.claude/commands/feature.md`](.claude/commands/feature.md).
The user can also invoke it explicitly via `/feature <description>`. The
playbook is non-negotiable: plan mode first, then offer the user three
choices (refine plan / write to GitHub issue / execute end-to-end with
branch + tests + commit + PR). Do not skip plan mode, do not start a
branch before the user picks execute, do not commit without `make test`
and `make lint` passing.

## Implementation contracts — load on demand

Read the relevant file before changing code in that area. Each is a
focused contract; load only what you need.

| File | Load when |
|---|---|
| [`.claude/context/architecture.md`](.claude/context/architecture.md) | Repo layout, library-first rule, surface parity, manager↔broker contract, filesystem (afero), descriptor envelopes |
| [`.claude/context/channels.md`](.claude/context/channels.md) | Channel name grammar, lifecycle, TTL |
| [`.claude/context/errors.md`](.claude/context/errors.md) | Coded errors (`PARSEC_*`), RPC mapping |
| [`.claude/context/auth.md`](.claude/context/auth.md) | JWT, key rotation, scopes, OIDC, refresh-token flow |
| [`.claude/context/tokenbroker.md`](.claude/context/tokenbroker.md) | Token broker — issuance, ACL, revocation |
| [`.claude/context/cache.md`](.claude/context/cache.md) | Request-hash cache |
| [`.claude/context/sinks.md`](.claude/context/sinks.md) | Sink retry policy + DLQ |
| [`.claude/context/ratelimit.md`](.claude/context/ratelimit.md) | Per-key sliding-window budgets |
| [`.claude/context/observability.md`](.claude/context/observability.md) | Metrics, tracing, access logs, telemetry aggregator |
| [`.claude/context/surfaces.md`](.claude/context/surfaces.md) | CLI, Twirp, WebSocket, HTTP-stream, SSE, JS client |

## Build, test, lint

```bash
make build            # bin/parsec
make test             # go test ./...
make lint             # go vet + staticcheck
make cover            # coverage profile
make proto            # regenerate rpc/ from service.proto (requires protoc + protoc-gen-twirp)
make docs             # mdbook build
```

CGO is disabled in the Makefile. Anything that pulls a C toolchain
fails the build. `make lint` + `make test` are auto-enforced at turn
end via [`.claude/hooks/enforce-checks.sh`](.claude/hooks/enforce-checks.sh).

## The Update Demand

Any change to a public surface MUST update its docs in the same PR.

| Trigger | Update |
|---|---|
| New CLI subcommand | `docs/src/cli/` page + `docs/src/SUMMARY.md` |
| New RPC method | `rpc/service.proto` + run `make proto` (commit generated `service.pb.go` + `service.twirp.go`) + adapter method in `service/adapter.go` (or `service/keys_adapter.go`) + `internal/rpcclient/` + `internal/cli/` |
| New channel namespace | `docs/src/channels/naming.md` + a test in `channels/name_test.go` |
| New error code | `errors/codes.go` + server-side mapping in `service/twirp_errors.go` + client-side mapping in `internal/rpcclient/errors.go` + `clients/js/src/errors.ts` `ParsecErrorCode` enum + Twirp mapper + a row in `clients/js/test/errors.test.ts` |
| New sink | `sinks/<name>/` package + manifest exposure via registry + MUST declare transient/terminal classification by wrapping errors with `sinks.Transient(err)` / `sinks.Terminal(err)`; sinks with a per-call recipient MUST implement `sinks.RecipientDecoder` so Redis-DLQ Replay can rebuild the typed Recipient |
| New auth token type | `auth/claims.go` (Type const + Valid()) + verifier round-trip test + manifest exposure if applicable |
| New key role | `auth/keyring.go` Role const + tests + `keyring.json` format_version bump if persisted differently |
| New key-management RPC | `rpc/service.proto` + run `make proto` + `service/keys.go` + `service/keys_adapter.go` + `internal/rpcclient/keys.go` + `internal/cli/keys.go` |
| New scope verb | `auth/scope.go` (Verb const + `Valid()` + `AllVerbs`) + verb-gating branch in `Scope.Authorizes` + truth-table test in `auth/scope_test.go` + manifest exposure via `descriptor.Manifest.SupportedVerbs` + `docs/src/channels/acl.md` |
| New subsystem with state | A Prometheus collector in `internal/metrics/registry.go` (or a documented justification for none) + entry in `docs/src/ops/observability.md` metric reference |
| New ingress point | `service.Service` call site consulting `Parsec.CheckRateLimit` before doing work; documented bucket key; entry in `docs/src/ops/rate-limiting.md` |
| New telemetry Snapshot field | `telemetry/telemetry.go` Source method + Aggregator sum branch + corresponding `parsec_telemetry_*` gauge in `telemetry/prom.go` + metric-reference row in `docs/src/ops/telemetry-alerts.md` |
| New severity level | `telemetry/alerts.go` Severity const + `Valid()` branch + table row in `docs/src/ops/telemetry-alerts.md` |
| New `tokenbroker.RevocationStore` impl | implement all four interface methods + apply `MaxTTL` semantics + tests for token-scope + user-scope round trips + entry in `docs/src/ops/token-broker.md` |
| New `cache.Cache` backend | implement all 7 interface methods + return a stable `cache.Stats` shape + optional `cache.BackendReporter` for manifest label + entry in the backend table in `docs/src/ops/cache.md` |
| Change to `channels/name.go` grammar | port to `clients/js/src/channels.ts` in the same PR + mirror the new test case in `clients/js/test/channels.test.ts` |
| Bump `descriptor.FormatVersion` | update `clients/js/src/manifest.ts` envelope parser + `clients/js/src/types.ts` Envelope/Manifest shape + a fixture refresh in `clients/js/test/manifest.test.ts` |
| New transport mounted in `internal/server/` | add to the `TransportName` union in `clients/js/src/types.ts` + extend `clients/js/src/manifest.ts` `transportPath` + doc page row in `docs/src/getting-started/js-client.md` |
| Refresh-token RPC wire change | update `RefreshTokenRequest`/`RefreshTokenResponse` in `clients/js/src/types.ts` + `clients/js/src/refresh.ts` mapping + a refresh fixture in `clients/js/test/refresh.test.ts` |

## Anti-patterns to refuse

- A new `internal/` package that duplicates `service/` logic.
- A CLI command that calls `broker.*` or `channels.*` directly. Go through
  `service.Service`.
- Error strings instead of coded errors at a package boundary.
- A new channel-validation helper outside `channels/`.
- Adding a feature to Parsec that the centrifuge OSS lib cannot deliver. The
  reflex should be: "is this a primitive parsec should ship, or a sink the
  consumer should compose?"
- A JS-client feature the Go server cannot also model. The wrapper transports
  policy, never invents it. If `clients/js/` grows authorization or schema
  decisions, the decision belongs in `service/` and the client just plumbs
  the verdict.
- A second JavaScript package in this repo. One client, one wrapper. New
  language clients live in `clients/<lang>/` — not under a separate
  `clients/js-extras/` or `clients/js2/`.
- A `clients/js/src/` module that imports `centrifuge` types into the
  `types.ts` public surface. Scope-inspector-only consumers must be able
  to import `types.ts` without dragging centrifuge-js into the type graph.
