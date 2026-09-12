.PHONY: build clean test test-integration cover fmt fmt-check vet lint proto docs docs-serve docs-clean playground

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

# SUBMODULES are the nested Go modules in this repo. They exist to keep a
# heavy SDK out of the root module's dependency graph, which only works if
# they are built, tested and linted like everything else — `go test ./...`
# in the root module does not descend into a nested module.
SUBMODULES=stores/gcpsecretmanager

build:
	$(GO) build $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/parsec
	$(GO) build $(BUILD_FLAGS) -o $(BUILD_DIR)/parsec-gen ./cmd/parsec-gen
	@for m in $(SUBMODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) build ./...) || exit 1; \
	done

clean:
	rm -rf $(BUILD_DIR) coverage.out

test:
	$(GO) test ./...
	@for m in $(SUBMODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) test ./...) || exit 1; \
	done

# test-integration runs the full suite with the race detector enabled and
# a tightened timeout. Designed for CI; locally `make test` is faster.
# The race detector requires CGO, so this target overrides the
# CGO_ENABLED=0 default applied above.
test-integration:
	CGO_ENABLED=1 $(GO) test -race -count=1 -timeout=120s ./...
	@for m in $(SUBMODULES); do \
		echo "==> $$m"; \
		(cd $$m && CGO_ENABLED=1 $(GO) test -race -count=1 -timeout=120s ./...) || exit 1; \
	done

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

fmt:
	$(GO) fmt ./...
	@for m in $(SUBMODULES); do \
		(cd $$m && $(GO) fmt ./...) || exit 1; \
	done

# fmt-check is the read-only counterpart of `fmt`: same rules, no writes.
# Two details matter here.
#
# `gofmt -l` exits 0 whether or not it lists anything, so the file list has
# to be converted into an exit status by hand.
#
# gofmt comes from the active toolchain's GOROOT rather than PATH. A PATH
# gofmt can be a different Go release than the one building the code, which
# produces the worst kind of failure: clean locally, dirty in CI.
fmt-check:
	@files="$$($$($(GO) env GOROOT)/bin/gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "not gofmt-clean:"; \
		echo "$$files" | sed 's/^/  /'; \
		echo "run 'make fmt' to fix"; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...
	@for m in $(SUBMODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) vet ./...) || exit 1; \
	done

# fmt-check runs first: neither vet nor staticcheck looks at formatting,
# which is how 32 files drifted out of format unnoticed.
lint: fmt-check vet
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...
	@for m in $(SUBMODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...) || exit 1; \
	done

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

# Boot an in-memory parsec + tabbed browser UI for poking at every
# surface (publish, subscribe, tokens, sinks, DLQ, rate limit,
# manifest). Developer aid only — bearer auth is disabled.
playground:
	$(GO) run ./examples/playground

.DEFAULT_GOAL := build
