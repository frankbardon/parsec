.PHONY: build clean test test-integration cover fmt vet lint proto docs docs-serve docs-clean

BINARY_NAME=parsec
BUILD_DIR=bin
GO=go
LDFLAGS=-s -w
BUILD_FLAGS=-trimpath -ldflags="$(LDFLAGS)"

# Parsec is pure Go — no CGO dependency in the build graph. Disabling CGO
# globally makes that a contract: any future import that pulls in a C
# toolchain fails the build instead of silently re-introducing the
# dependency. Override on the command line if a downstream consumer
# really needs CGO.
export CGO_ENABLED=0

ifneq (,$(wildcard ./.env))
    include .env
    export
endif

build:
	$(GO) build $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/parsec

clean:
	rm -rf $(BUILD_DIR) coverage.out

test:
	$(GO) test ./...

# test-integration runs the full suite with the race detector enabled and
# a tightened timeout. Designed for CI; locally `make test` is faster.
test-integration:
	$(GO) test -race -count=1 -timeout=120s ./...

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

lint: vet
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...

# Regenerate protobuf + Twirp bindings from rpc/service.proto.
# Requires `protoc`, `protoc-gen-go`, and `protoc-gen-twirp` on PATH.
proto:
	protoc -I=./rpc \
	  --go_out=./rpc --go_opt=paths=source_relative \
	  --twirp_out=./rpc --twirp_opt=paths=source_relative \
	  ./rpc/service.proto

docs:
	mdbook build docs

docs-serve:
	mdbook serve docs --open

docs-clean:
	rm -rf docs/book

.DEFAULT_GOAL := build
