# Running with Docker

Parsec ships a multi-arch (`linux/amd64`, `linux/arm64`) container
image. The Dockerfile builds a fully static binary on
`golang:1.26.1-alpine` and ships it on `gcr.io/distroless/static-debian12`
— the result is ~7 MB total, no shell, no package manager, no CVE
surface beyond the binary itself.

Tagged releases publish to `ghcr.io/frankbardon/parsec`:

| Tag | Tracks |
|---|---|
| `latest` | Most recent tagged release |
| `vX.Y.Z` | Specific semver release |
| `vX.Y` | Latest patch of the minor |
| `vX` | Latest minor of the major |
| `main` | Tip of `main` branch (unstable) |
| `sha-<short>` | Specific commit |

## One-shot run

```bash
docker run --rm -p 8000:8000 ghcr.io/frankbardon/parsec:latest
# parsec serve: bootstrap mgmt token (expires 2026-05-23 18:04:11 UTC):
# eyJhbGciOiJIUzI1NiIs...
```

The bootstrap mgmt token is on stderr. Capture it from the container
logs:

```bash
docker logs <container-id> 2>&1 | grep bootstrap
```

Persistent keyring (recommended):

```bash
docker run --rm -p 8000:8000 \
  -v parsec-state:/var/lib/parsec \
  ghcr.io/frankbardon/parsec:latest
```

The image's default `CMD` is
`serve --addr :8000 --state-dir /var/lib/parsec`. Override with any
parsec subcommand:

```bash
docker run --rm ghcr.io/frankbardon/parsec:latest --version
docker run --rm ghcr.io/frankbardon/parsec:latest channels list \
  --server http://parsec.internal:8000 \
  --token "$PARSEC_TOKEN"
```

## docker compose

The repo ships a `docker-compose.yml` that boots parsec + Redis
together. It mounts `examples/config/parsec.docker.yaml` into the
container so the broker, channel registry, keyring, DLQ, and rate
limiter all run against Redis — the same multi-node code paths
production uses.

```bash
git clone https://github.com/frankbardon/parsec.git
cd parsec
docker compose up --build
```

Once both services are healthy:

```bash
docker compose logs parsec | grep bootstrap
# capture the mgmt token, export PARSEC_TOKEN, then:
parsec channels list   # (or use the in-container CLI)
```

Drop the state volume to reset auth:

```bash
docker compose down -v
```

## Building locally

```bash
docker build -t parsec:dev .
docker run --rm -p 8000:8000 parsec:dev
```

The build is cached on `go.mod` / `go.sum`, so iteration on Go source
typically reuses the module-download layer.

To override the version stamp baked into the binary:

```bash
docker build --build-arg VERSION=v0.1.0 -t parsec:v0.1.0 .
```

## Health checks

The image exposes a `/healthz` endpoint that returns `200 OK` once the
HTTP listener is bound. Wire it into your orchestrator:

```yaml
livenessProbe:
  httpGet: { path: /healthz, port: 8000 }
  initialDelaySeconds: 2
  periodSeconds: 10
```

For Kubernetes deployments, also gate the bearer middleware by setting
`PARSEC_METRICS_TOKEN` and scraping `/metrics` from your monitoring
namespace.

## Image surface

- **User**: `nonroot` (uid 65532, gid 65532). Drops root from the
  container at start; required for restricted PSP/PSA environments.
- **Entrypoint**: `/usr/local/bin/parsec`. The image has no shell —
  `docker exec -it ... sh` will fail. Use a sidecar for debugging.
- **Ports**: `8000/tcp` (HTTP / WebSocket / Twirp). WebTransport (HTTP/3)
  uses UDP on a separate listener; expose it as needed.
- **Volumes**: `/var/lib/parsec` (keyring + any future state).
- **Labels**: Standard OCI labels point at the source repo + license.

## Security notes

- Always set `PARSEC_METRICS_TOKEN` (env or YAML) when `/metrics` is
  reachable from outside the cluster. The default is unguarded so dev
  evaluation works out of the box.
- Always set `--state-dir` on a persistent volume in production. An
  ephemeral keyring means every container restart issues a new
  bootstrap mgmt token and invalidates every outstanding access /
  refresh token.
- The image is rebuilt on every tagged release. Pin the digest in
  production (`ghcr.io/frankbardon/parsec@sha256:...`) so an unrelated
  retag does not change what you deploy.
