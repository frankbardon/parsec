# syntax=docker/dockerfile:1.7

# Multi-stage build: compile parsec on golang-alpine, ship on a
# distroless static base (~2 MB + binary). CGO stays disabled so the
# binary is fully static — distroless static has no libc.

ARG GO_VERSION=1.26.1

FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

# Cache module downloads when only source changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/parsec ./cmd/parsec

# ---- runtime image ----
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="parsec"
LABEL org.opencontainers.image.description="Scalable realtime messaging engine"
LABEL org.opencontainers.image.source="https://github.com/frankbardon/parsec"
LABEL org.opencontainers.image.licenses="MIT"

# State directory for the persistent keyring. The nonroot user (uid 65532)
# owns it so `--state-dir /var/lib/parsec` works out of the box.
COPY --from=build --chown=nonroot:nonroot /out/parsec /usr/local/bin/parsec
WORKDIR /var/lib/parsec

EXPOSE 8000
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/parsec"]
CMD ["serve", "--addr", ":8000", "--state-dir", "/var/lib/parsec"]
