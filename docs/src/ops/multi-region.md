# Multi-Region Deployments

Parsec is a single-region service by default. The **minimum** tooling
needed to run N regions and keep the auth surface coherent ships in the
box — every region runs its own self-contained Parsec cluster (broker,
manager, DLQ, rate limiter, Redis) and clients connect to their nearest
one.
Tokens issued in `us-east` must verify in `eu-west`; a key retired in
one region must stop accepting tokens in every other region within a
second.

Full active-active broker replication (cross-region channel history,
conflict resolution, CRDT-Redis) is **deferred to v0.4+**. This page
covers what you can do today.

## When NOT to use multi-region

Single-region is almost always simpler. Use it when:

- Your client base is regional (e.g. a US-only app).
- Cross-region latency for the auth surface is acceptable. A US-based
  client doing one token issue per session can tolerate a 200ms
  round-trip to a single Parsec cluster.
- You do not yet have a regional Redis fleet. Parsec multi-region
  assumes each region runs its own Redis (and that you operate one).

Add a second region when:

- p95 connect/subscribe latency is dominated by the trans-oceanic
  hop. Clients on three continents will feel it.
- You have a regulatory requirement to keep customer traffic local
  (e.g. EU data residency for EU users).
- You already run a multi-region service mesh and want Parsec to live
  next to your existing surfaces.

## Topology

```
                           ┌────────────────┐
                           │   Operator     │
                           │   (you)        │
                           └───────┬────────┘
                                   │  parsec keys retire k-old
                                   │  parsec keys export | scp …
                                   │
              ┌────────────────────┼────────────────────┐
              ▼                    ▼                    ▼
        ┌──────────┐         ┌──────────┐         ┌──────────┐
        │ us-east  │         │ eu-west  │         │ ap-south │
        │ Parsec   │         │ Parsec   │         │ Parsec   │
        │   ↕      │         │   ↕      │         │   ↕      │
        │ Redis    │         │ Redis    │         │ Redis    │
        └──────────┘         └──────────┘         └──────────┘
              ▲                    ▲                    ▲
              │                    │                    │
              └─── US clients      │                    │
                                EU clients         APAC clients
```

Each region is independent for channels, presence, history, sinks, and
DLQ. The **only** cross-region coherence point is the keyring — every
region must verify the same kid set so a token minted in `us-east`
verifies in `eu-west`.

## Configuration

### Region tag

`Options.Region` (or `region:` in the YAML config) labels every
Prometheus metric and every access-log line with the operator's region
name. Empty value (single-region default) attaches **no** label — the
scrape and log shape stays lean for single-region deployments.

```yaml
region: us-east
peers:
  - https://parsec.eu-west.example.com:8000
  - https://parsec.ap-south.example.com:8000
```

`peers:` is informational — Parsec does not call into peers
automatically. The list surfaces in the manifest so operators (and
tooling like `parsec-keys-sync`) can discover topology.

Prometheus example query (after configuring two regions):

```promql
sum by (region) (rate(parsec_publishes_total[1m]))
```

### Backward compatibility

Existing single-region deployments need no changes. The new fields
default to empty / nil, the manifest omits them (`omitempty`), and the
metrics scrape stays unlabeled.

## Sharing keyrings between regions

Three patterns are supported. Pick based on your network topology.

### Pattern 1 — Push via the CLI

The operator generates and promotes a key in region A, exports the
ring, and imports it into every peer.

```bash
# Region A (the source)
parsec keys generate
parsec keys promote k-abcdef123456
parsec keys export --state-dir /var/lib/parsec > /tmp/keyring.json

# For every peer region
scp /tmp/keyring.json eu-west:/tmp/
ssh eu-west 'parsec keys import --state-dir /var/lib/parsec < /tmp/keyring.json'
```

For Redis-backed deployments:

```bash
parsec keys export --redis-addr us-east-redis:6379 > /tmp/keyring.json
parsec keys import --redis-addr eu-west-redis:6379 < /tmp/keyring.json
```

`keys import` calls `KeyRingStore.Save` which (for Redis) bumps the
version key and publishes the rotation event, so every parsec node in
the target region reloads through its existing watch goroutine.

#### Collision safety

`keys import` refuses to overwrite an existing kid whose secret
differs:

```
PARSEC_INVALID_ARGUMENT: key id "k-abcdef" present locally with a
different secret; pass --force to overwrite
```

This protects against the "two regions independently generated the
same kid" pathological case. Pass `--force` only when you intend to
overwrite — for example, when migrating from independently-bootstrapped
regions to a shared ring.

### Pattern 2 — Push via the notify hook

When you retire a key, propagate the retirement to every peer in one
command:

```bash
parsec keys retire k-old \
  --notify https://parsec.eu-west.example.com:8000 \
  --notify https://parsec.ap-south.example.com:8000
```

The CLI does the local retire first, then POSTs to each peer's
`/twirp/parsec.ParsecService/ReloadKeys` with the operator's current
bearer (so peers must trust the same kid via a shared keyring — usually
via Pattern 1 first, or via a shared OIDC issuer from
[Phase 21](../auth/oidc.md)).

Individual peer failures log a warning but **do not roll back** the
local retire — propagation is best-effort. The envelope output
includes a `notified` array with per-peer status so you can spot which
region drifted.

### Pattern 3 — Pull via `parsec-keys-sync`

Run the standalone `parsec-keys-sync` daemon next to each region's
Redis. It subscribes to the source region's `<prefix>:keyring:events`
pub/sub channel and re-publishes every payload to every target region's
Redis.

```bash
parsec-keys-sync \
  --from us-east-redis:6379 \
  --to eu-west-redis:6379 \
  --to ap-south-redis:6379 \
  --key-prefix parsec
```

The daemon is stateless and idempotent — re-publishing an already-
applied version is a no-op on the target side (the target's parsec
nodes load the snapshot and see no change). Restart the daemon with the
same flags after any crash; there's nothing to recover.

A common deployment is one daemon per source region, each forwarding
to every other region. With three regions you run three daemons, each
with two `--to` targets.

#### Operating notes

- The daemon is a **separate binary** at `cmd/parsec-keys-sync/`.
  Build it from the parsec repo: `go build -o bin/parsec-keys-sync
  ./cmd/parsec-keys-sync`.
- Logs every forwarded event at INFO. Forwarding failures log at
  WARN and the daemon keeps running — a flapping target does not
  affect the others.
- SIGTERM / SIGINT cause a graceful shutdown (cancel context, unwind
  the subscriber, exit 0).
- The daemon needs network access to **all** Redis instances. Place
  it accordingly in your VPC peering / firewall rules.

## Push vs Pull — picking a pattern

| Question                                            | Use   |
| ---                                                 | ---   |
| Do operators have a shell on every region?          | Push  |
| Do you want a single command to propagate retires?  | Push  |
| Are regional Redis instances on a shared L7 mesh?   | Pull  |
| Do you want automated continuous mirroring?         | Pull  |
| Can you tolerate operator-driven manual propagation? | Push  |

The two patterns are complementary. A common combination: bootstrap
with Pattern 1 (single export+import on day 1), then run Pattern 3
continuously so rotations propagate automatically.

## Latency considerations

- **Key rotation** propagates at the speed of your slowest link.
  Pattern 2 (notify) is HTTP RPC over your existing load balancers;
  expect 50ms to 300ms depending on geography. Pattern 3 (keys-sync)
  is a Redis publish; same-DC publishes complete in microseconds,
  trans-oceanic publishes in 100-200ms.
- **Token verification** is local — the verifying region never
  reaches across to the issuing region. As long as both regions have
  the same kid in their ring (via export/import or keys-sync), a token
  minted in us-east verifies in eu-west with zero cross-region
  latency.
- **Connect / subscribe / publish** are entirely region-local. There
  is no broker replication; a publish to `public:foo.bar` in us-east
  is **not** seen by subscribers in eu-west. Clients that need cross-
  region fanout should subscribe to the same logical channel on every
  region they care about, or wait for v0.4 active-active replication.

## Failure modes

| Scenario                                | Behavior                                                                                                     |
| ---                                     | ---                                                                                                          |
| One region's Parsec is down             | Clients re-route to the next closest region (operator's load balancer / DNS). Tokens still verify there.     |
| Peer region unreachable during `--notify` | The local retire still succeeds. The unreachable region keeps verifying the old kid until reachable + reloaded. |
| `parsec-keys-sync` crashes              | No data loss — restart with the same flags. Rotations missed during downtime can be triggered manually with `parsec keys reload` on the target region.    |
| Two regions promote different keys      | The last `import` wins. Prefer Pattern 1 (operator-driven) for promotion; promotion is a deliberate operator action, not a rotation event. |

## Out of scope

- Active-active broker replication (channel state, history, presence).
  Deferred to v0.4+.
- DNS-based region routing — handle with your existing load balancer
  or CDN (GeoDNS, AWS Global Accelerator, Cloudflare Workers, etc.).
- Region failover automation — handle with your existing orchestrator
  (Kubernetes Service mesh, Consul, etc.).
- Cross-region rate-limit coordination. Each region's Redis hosts its
  own rate-limit state; a publisher hitting two regions could in
  theory exceed the configured rate. For most deployments this is
  acceptable — the per-region ceiling is still enforced.
