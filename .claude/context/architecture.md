# Architecture — Parsec

Load when changing repo layout, package boundaries, or wiring between
the library facade, CLI, and RPC server.

## Repository layout

```
parsec.go              # public library facade
auth/                  # HMAC-SHA256 JWT signer/verifier/issuer + subscribe authorizer
broker/                # centrifuge.Node wrapper
channels/              # name grammar + lifecycle manager + TTL
sinks/                 # Sink iface + email / slack / webhook impls
service/               # surface-agnostic business logic (used by CLI, RPC)
rpc/                   # Twirp JSON wire — generated from rpc/service.proto (pb.go + twirp.go)
descriptor/            # manifest envelope
errors/                # coded errors (PARSEC_*)
internal/cli/          # CLI command bodies
internal/codegen/      # parsec-gen Go + TS emitters (driven from schema registry)
internal/server/       # HTTP mux (twirp + websocket + sse + healthz)
internal/rpcclient/    # CLI adapter onto the generated Twirp JSON client
stores/                # adapters in their own Go modules, one per heavy SDK
  gcpsecretmanager/    # auth.KeyRingStore backed by Google Secret Manager
cmd/parsec/            # main.go assembler
cmd/parsec-gen/        # codegen binary; reads schema registry, emits Go/TS bindings
docs/                  # mdBook source
```

## Nested modules

An adapter whose SDK would bloat the root module's dependency graph lives
under `stores/<name>/` as its own Go module, depending on parsec rather
than the other way round. Nothing in the root module may import one — the
embedder constructs the adapter and passes it in through `Options`.

`go test ./...` does not descend into a nested module, so every new one
goes into the Makefile's `SUBMODULES` list or it is never built, tested or
linted by CI. Until parsec has a published tag, each carries a `replace`
pointing at the checkout.

## Library-first

All business logic lives in library packages. `cmd/parsec/main.go` and
`internal/cli/*` only parse flags and call into the library. If a CLI
subcommand grows logic that the library cannot also call, the CLI is wrong.

## Surface parity

Anything reachable over Twirp must also be reachable from the CLI.
The Twirp service definition in `rpc/service.proto` is the contract;
`rpc/service.pb.go` and `rpc/service.twirp.go` are generated from it by
`make proto` and are the source of truth for the wire format. Edit the
proto, then regenerate — never edit the generated files by hand.

## No business logic in `cmd/`

`cmd/parsec/main.go` assembles the command tree. Nothing else.

## Filesystem access

Library code uses `afero.Fs`. Never `os.Open` / `os.ReadFile` inside library
packages. The CLI is the one allowed exception (e.g. reading a `--file`
argument).

## Descriptor envelopes

Every JSON output (CLI `--json`, `/manifest`) wraps its payload in
`descriptor.Envelope`. The format version is bumped on any wire-shape
change.

## Manager ↔ broker contract

The channel manager owns lifecycle. It emits `Event{Kind, Name, At}` on
every transition; the broker translates events into wire actions.

| Event | Wire effect |
|---|---|
| `opened` | New subscribers accepted (subject to auth) |
| `closed` (public, TTL exceeded) | No new arrivals; existing subscribers drain — NOT kicked |
| `deleted` | Every connected subscriber is unsubscribed |

`Manager.Subscribe(chan<- Event)` registers a consumer; the manager does
non-blocking sends, so use a buffered channel. `parsec.New` composes the
subscribe authorizer so closed/deleted channels reject new subscribes
even before the bridge runs.
