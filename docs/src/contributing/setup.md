# Development setup

How to get a working Parsec checkout and run the development loop.

```bash
git clone https://github.com/frankbardon/parsec.git
cd parsec
make build
make test
```

## Prerequisites

- Go 1.26.1 or newer. The go.mod declares the toolchain; an older
  compiler will refuse to build.
- A POSIX shell (Linux or macOS).
- `make`. The repo uses GNU make for the development loop.
- `mdbook` (optional) — only required for `make docs`.
- No CGO, no protoc by default. The `proto` target needs `protoc`,
  `protoc-gen-go`, and `protoc-gen-twirp` only when you regenerate
  the RPC bindings.

Verify your Go install:

```bash
go version
# go version go1.26.1 darwin/arm64
```

## Clone

```bash
git clone https://github.com/frankbardon/parsec.git
cd parsec
```

The module path is `github.com/frankbardon/parsec`. The repo follows
the standard Go layout — `cmd/` for binaries, `internal/` for non-API
packages, every other top-level directory is a public library
package.

## Build

```bash
make build
# bin/parsec
```

This runs `go build -trimpath -ldflags="-s -w" -o bin/parsec
./cmd/parsec` with `CGO_ENABLED=0`. The Makefile owns the build flags;
the goal is a reproducible, statically linkable artifact.

Confirm the binary:

```bash
./bin/parsec --version
# parsec version dev
```

## Test

```bash
make test
# go test ./...
```

The full suite is fast (under a minute on a modern laptop). Per-package
tests are conventional Go tests; the channels manager and parsec
end-to-end suites use injected clocks rather than `time.Sleep` so the
suite stays deterministic.

For coverage:

```bash
make cover
# go test -coverprofile=coverage.out ./...
# go tool cover -func=coverage.out
```

## Lint

```bash
make lint
# go vet ./...
# go run honnef.co/go/tools/cmd/staticcheck@latest ./...
```

`make lint` chains `go vet` and `staticcheck`. The `staticcheck` call
is via `go run` so you don't need to install it separately. All new
code is expected to pass both. If `staticcheck` flags a lint you
believe is wrong, add a `//lint:ignore` with a brief rationale rather
than silently muting the rule.

## Format

```bash
make fmt
# go fmt ./...
```

Imports are managed by `gofmt`; do not hand-sort them.

## Documentation

```bash
make docs
# mdbook build docs
```

The mdBook source lives under `docs/src/`. Adding a new page means
adding a file under `docs/src/<section>/` AND adding a line to
`docs/src/SUMMARY.md`. The summary is the table of contents — pages
that exist on disk but are not in the summary are invisible.

For live preview while writing:

```bash
make docs-serve
# mdbook serve docs --open
```

## Protobuf (rarely)

```bash
make proto
# protoc + protoc-gen-go + protoc-gen-twirp
```

Only needed when the wire shape in `rpc/service.proto` changes. The
hand-rolled `rpc/types.go` is the contract until then.

## Submitting changes

- Branch from `main`.
- One logical change per PR. Keep diffs small.
- Run `make lint test docs` before opening the PR. The CI runs the
  same targets.
- Update the relevant doc page in the same PR — see "The Update
  Demand" in `CLAUDE.md` for the full mapping.

## See also

- [Style guide](style.md).
- The [architecture overview](../internals/architecture.md) for a
  package-by-package tour.
