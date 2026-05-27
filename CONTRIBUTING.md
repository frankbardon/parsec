# Contributing to Parsec

Thanks for your interest in contributing to Parsec! This guide covers the basics.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/<you>/parsec.git`
3. Create a branch: `git checkout -b my-feature`
4. Make your changes
5. Run checks: `make test && make lint`
6. Commit and push
7. Open a pull request

## Development Setup

- Go 1.26+
- `protoc`, `protoc-gen-go`, `protoc-gen-twirp` (only if you regenerate `rpc/`)
- `mdbook` (only if you build docs)

```bash
make build    # Build CLI binary to bin/parsec
make test     # Run tests
make lint     # Run staticcheck (includes vet)
make cover    # Run tests with coverage
make proto    # Regenerate rpc/ from service.proto
```

## Code Conventions

- **Library-first.** All channel, broker, and sink logic lives in library packages. The CLI in `cmd/parsec/` is a thin adapter.
- **No business logic in `cmd/parsec/`.** Parse flags, call library, format output.
- **Error codes** use `DOMAIN_CATEGORY` format and the typed `errors.Code` system (e.g. `PARSEC_CHANNEL_NOT_FOUND`, `PARSEC_TTL_EXCEEDED`).
- **Channel naming** follows the public/private namespace convention. See [docs/src/channels/naming.md](docs/src/channels/naming.md).
- **Surfaces are equivalent.** Anything available over Twirp must also be reachable from the CLI (with appropriate scoping).
- **No `fmt.Sprintf` for JSON.** Use `encoding/json` and `descriptor.NewEnvelope`.
- **All file I/O via `afero.Fs`.** Never `os.Open` directly in library code.

See [CLAUDE.md](CLAUDE.md) for the full set of conventions and contracts.

## The Update Demand

Any change to code, configuration, or public surface MUST update the corresponding docs in the same PR. CLI, Twirp methods, and channel conventions all have user-facing surfaces; their docs are not optional.

## Pull Request Guidelines

- Keep PRs focused — one feature or fix per PR
- Include tests for new functionality
- Update docs and CLAUDE.md per the Update Demand
- Run `make test && make lint` before submitting
- Fill out the PR template

## Reporting Bugs

Use the [bug report template](https://github.com/frankbardon/parsec/issues/new?template=bug_report.yml). Include the channel name, the request you made, your Go version, and OS.

## Suggesting Features

Use the [feature request template](https://github.com/frankbardon/parsec/issues/new?template=feature_request.yml). Describe the problem you're solving, not just the solution you want.
