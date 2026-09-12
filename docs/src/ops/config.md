# Configuration file

`parsec serve --config <path>` loads operator settings from a YAML file
so multi-node deployments and version-controlled rollouts have a single
artifact to track. The flag is also reachable via the `PARSEC_CONFIG`
environment variable.

## Precedence

```
CLI flag > env var > config file > built-in default
```

Any flag the operator passes explicitly wins over the file. Anything the
file doesn't set falls through to defaults baked into the binary. There
is no merging of structured fields — each scalar is settled at one
layer.

## Format

YAML. Reasons it's the default:

- Familiar to operators coming from Kubernetes, GitHub Actions, Docker
  Compose, and most other Go ecosystem libraries
- Anchors / aliases supported if you need them (rarely)
- Comments survive
- Parsed by `gopkg.in/yaml.v3` — the de facto Go YAML decoder

Strings support `${ENV_VAR}` interpolation so secrets can be sourced from
the environment without inlining them into the file:

```yaml
observability:
  metrics_bearer_token: "${PARSEC_METRICS_TOKEN}"
```

Unknown keys are rejected at load time — a typo in a key name fails
boot with a clear error rather than silently ignoring the setting.

## Reference

See `examples/config/parsec.yaml` in the repo for a fully-populated
configuration with comments explaining each field. The schema follows
the `parsec.Options` surface; every CLI flag has a matching file field.

### Sections

| Section | Purpose |
|---|---|
| `server` | Listen address, debug toggles |
| `auth` | Keyring location, bootstrap mgmt token settings |
| `redis` | Cross-node Redis coherence (channels + keyring + DLQ + rate limits) |
| `manager` | Channel manager tunables (sweep interval) |
| `sink_retry` | Global + per-sink retry policy |
| `rate_limits.*` | Publish / subscribe / token-issue buckets |
| `observability` | Metrics, tracing, access-log trusted proxies |

### `redis`

Setting `redis.addr` switches the channel registry, keyring, DLQ, rate
limiter and broker to their Redis-backed implementations. **It also makes
Redis the sole home of the signing keys** — `auth.state_dir` is ignored for
key storage, so read
[Redis durability](deployment.md#redis-durability) before deploying.

| Field | Default | Notes |
|---|---|---|
| `addr` | "" | `host:port`, or a `redis://`, `rediss://`, `tcp://` or `unix://` URL. May carry `user:pass@` and `/db`. Sentinel and cluster URLs are rejected — pass `Options.RedisClient` instead. Empty means single-node in-memory. |
| `key_prefix` | `parsec` | Namespace for every parsec key. Override when deployments share an instance. |
| `node_id` | auto | Identifier used to dedupe this node's own pub/sub echoes. |
| `username` | "" | Overrides any user in `addr`. |
| `password` | "" | Overrides any password in `addr`. Pair with `${VAR}` interpolation to keep it out of the file. |
| `db` | unset | Logical database. Omitted leaves whatever `addr` encoded; an explicit `0` forces database 0. |
| `tls.enabled` | `false` | Forces TLS on an address that does not imply it. `rediss://` already does. |
| `tls.ca_file` | "" | PEM bundle of roots for a private CA. |
| `tls.server_name` | "" | Name checked against the certificate, when the dial address is an IP or tunnel. |
| `tls.insecure_skip_verify` | `false` | Disables verification. Debugging only — the connection carries your signing keys. |

The equivalent CLI flags are `--redis-addr` and `--redis-key-prefix`
(`PARSEC_REDIS_ADDR`, `PARSEC_REDIS_KEY_PREFIX`). Credentials and TLS are
file-only, since they do not belong in a process listing.

## Mutual exclusions

The loader rejects impossible combinations at startup:

- A `rate_limits.<bucket>` with `rate > 0` must also set `per`
- A `redis.addr` that is neither `host:port` nor a supported URL
- `redis.username` / `redis.password` / `redis.tls.enabled` without a
  `redis.addr` — the credentials would be silently unused

Caught at boot, not at first request.

## Boot diagnostics

When `--config` is set, `parsec serve` logs `parsec serve: loaded
config path=<path>` on startup, and the manifest exposes a
`config_source` field with the same value so clients can confirm which
file is active:

```bash
curl -s -H "Authorization: Bearer $PARSEC_TOKEN" \
  http://localhost:8000/twirp/parsec.ParsecService/Manifest \
  | jq '.payload.config_source'
```

## Single-source-of-truth pattern

In production, prefer:

1. Commit a `parsec.yaml` per environment (`config/staging.yaml`,
   `config/prod.yaml`)
2. Reference secrets via `${ENV_VAR}` interpolation
3. Run with `parsec serve --config config/prod.yaml`
4. Override anything urgent via CLI flag — the file stays the
   long-running baseline; flags handle the incident-response case

## See also

- `examples/config/parsec.yaml` — reference file
- [Deployment](deployment.md) — single-node and multi-node patterns
- [Rate limiting](rate-limiting.md) — bucket semantics
- [Observability](observability.md) — metrics, tracing, log fields
