# Installation

How to get a working `parsec` binary on disk.

## Prerequisites

- Go 1.26.1 or newer. Parsec uses generics, structured logging, and the
  modern `slog` API; older toolchains will not build.
- A POSIX shell. The bootstrap message uses standard environment
  variables; nothing exotic.
- No CGO. The Makefile sets `CGO_ENABLED=0` and treats any new C-toolchain
  dependency as a build failure. You do not need a C compiler.

Verify your toolchain:

```bash
go version
# go version go1.26.1 darwin/arm64
```

## Install with `go install`

The fastest path. Drops the binary in `$(go env GOBIN)` (or
`$(go env GOPATH)/bin` if `GOBIN` is unset):

```bash
go install github.com/frankbardon/parsec/cmd/parsec@latest
```

Confirm:

```bash
parsec --version
# parsec version dev
```

The `dev` string is the default `ldflags` version stamp. Tagged releases
substitute the tag at build time.

## Build from source

Clone the repo and use the Makefile. The resulting binary lands at
`bin/parsec`:

```bash
git clone https://github.com/frankbardon/parsec.git
cd parsec
make build
./bin/parsec --version
```

`make build` runs:

```
go build -trimpath -ldflags="-s -w" -o bin/parsec ./cmd/parsec
```

`-trimpath` strips local build paths from the binary;
`-ldflags="-s -w"` drops the DWARF debug info and symbol table to shrink
the artifact. CGO is disabled by the Makefile.

## Run the test and lint targets

The same Makefile drives the rest of the development loop:

```bash
make test       # go test ./...
make lint       # go vet + staticcheck
make cover      # coverage profile
make docs       # mdbook build (requires mdbook on PATH)
```

`make lint` shells out to `staticcheck` via `go run`; it does not need a
separate install step.

## What you should do next

- Walk through the [quick start](quickstart.md) to boot the broker and
  publish your first message.
- Skim the [channel naming convention](../channels/naming.md) — every
  surface enforces it, so it pays to internalize early.
- If you intend to embed Parsec, jump to the
  [library overview](../library/overview.md).
