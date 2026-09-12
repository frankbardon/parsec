# Keyring in Google Secret Manager

The JWT signing keys can live in Google Cloud Secret Manager instead of
`keyring.json` or Redis. Every node in the fleet reads the same ring,
rotations propagate without a SIGHUP, and the keys sit under the same
IAM, audit log, CMEK and residency policy as the rest of your secrets —
with no writable disk and no Redis durable enough to be trusted with key
material.

It ships as a **separate Go module**, `stores/gcpsecretmanager`, so the
Google Cloud SDK stays out of the dependency graph of every deployment
that does not use it:

```bash
go get github.com/frankbardon/parsec/stores/gcpsecretmanager
```

## Wiring it up

The store is a library option — it takes a constructed client, which no
CLI flag can spell — so it is wired in an embedder's `main`:

```go
store, err := gcpsecretmanager.New(ctx, gcpsecretmanager.Config{
        ProjectID:       "my-project",
        SecretID:        "parsec-keyring",          // default
        CredentialsFile: "/var/run/secrets/parsec-sa.json",
})
if err != nil {
        return err
}
defer store.Close()

p, err := parsec.New(parsec.Options{KeyRingStore: store})
```

`parsec.New` bootstraps the ring through the store (minting one if the
secret is empty), reloads it when another node rotates, and persists
every `parsec keys` mutation back to it.

`Options.KeyRingStore` outranks `RedisClient` and `StateDir` — see the
[precedence table](deployment.md#keyring-stores). Setting `StateDir` as
well logs a warning: `keyring.json` is neither read nor written, so it is
not the backup it looks like.

## Credentials

| You have | Do this |
|---|---|
| Workload identity (GKE, Cloud Run, GCE) | Set neither field — Application Default Credentials are used |
| A service account key file | `CredentialsFile: "/path/to/sa.json"` |
| A service account key from another secret store | `CredentialsJSON: raw` |
| Anything else (impersonation, a custom endpoint) | `ClientOptions: []option.ClientOption{…}` |

When `CredentialsFile` or `CredentialsJSON` is set it must be a **service
account** key. The credential type is pinned rather than sniffed from the
file: a credential configuration swapped for an external-account one
would otherwise send this node's token requests to whatever URL that file
names, and it would look like it worked.

## IAM

Grant on the one secret. `roles/secretmanager.admin` covers everything;
a least-privilege binding needs:

| Permission | Why |
|---|---|
| `secretmanager.versions.access` | read the ring |
| `secretmanager.versions.add` | write a rotation |
| `secretmanager.versions.get` | the reconcile poll |
| `secretmanager.secrets.get` | read the write lease |
| `secretmanager.secrets.update` | take and release the write lease |
| `secretmanager.secrets.create` | only to create the secret on first boot |
| `secretmanager.versions.list`, `.destroy` | only when `RetainVersions` is set |

Creating the secret out of band and leaving `secretmanager.secrets.create`
off the binding is supported — the store uses the secret it finds.

## How the ring is stored

One secret, one JSON payload, a version per rotation:

| Resource | Holds |
|---|---|
| `projects/<project>/secrets/<secret-id>` | the keyring |
| `.../versions/N` | revision N of the ring |
| annotation `parsec-keyring-lock` | the short-lived write lease |

The version number is the revision, and it is what
`parsec_keyring_version` reports. Nodes serving the same ring report the
same number.

Payloads carry a CRC32C checksum in both directions, so a payload that
changed underneath the store is rejected rather than parsed.

## Concurrent rotation

Secret Manager appends versions unconditionally and the newest one wins
for every reader. Two nodes rotating at the same moment would both
append, and the second would erase the first — successfully, as far as
its caller could tell. Two nodes booting against an empty project would
each mint a ring, and the loser would sign tokens the rest of the fleet
rejects.

`Save` closes both windows:

1. It takes a write lease, written as a secret annotation with the
   secret's etag as the precondition. `UpdateSecret` honors that etag as
   a compare-and-set — the one atomic primitive the API offers.
2. Under the lease it re-reads the latest version number and refuses with
   `auth.ErrKeyRingConflict` when that is no longer the revision this node
   loaded. Parsec reloads, re-applies the mutation, and retries.
3. It appends the new version, then drops the lease.

A node that dies mid-write does not wedge the fleet: the lease carries a
deadline (`LockTTL`, default 30s), and the next writer takes it over.
Stealing it is itself an etag compare-and-set, and the revision check
still runs underneath, so an expired lease never costs correctness.

## Rotation latency

Secret Manager has no change feed a client can subscribe to — its
rotation notifications go to Pub/Sub, which would make every deployment
provision a topic and a subscription. The store polls the latest version
number instead, so `ReconcileInterval` (default 30s) is how long a node
can serve a ring another node has already replaced. The poll reads
version metadata, not the payload.

A failed poll is reported through the error hook — counted on
`parsec_keyring_reconcile_errors_total` — and retried on the next tick.
The watch itself never gives up: a node that stopped watching would serve
keys that can never be updated again, with one log line as the only
symptom.

## Versions and backups

Every rotation adds a version and **nothing is destroyed by default**. A
destroyed version is unrecoverable, and the previous ring is the only way
back from a bad rotation, so pruning is opt-in:

```go
Config{RetainVersions: 10}   // destroy all but the 10 most recent, after each write
```

Secret Manager holds the only copy of the signing keys. Losing the secret
is losing every token minted under it — including the bootstrap mgmt
token. Treat it like any other secret of record: restrict who can destroy
versions, and keep the project's audit log for it.

## Configuration reference

| Field | Default | Notes |
|---|---|---|
| `ProjectID` | — | required; never inferred from the credentials |
| `SecretID` | `parsec-keyring` | |
| `CredentialsFile` / `CredentialsJSON` | ADC | service account key; mutually exclusive |
| `ReconcileInterval` | `30s` | rotation latency across the fleet; negative disables the poll |
| `LockTTL` | `30s` | how long this node's write lease is honored |
| `LockWait` | `10s` | how long `Save` waits on another node's lease |
| `ReplicaLocations` | automatic | user-managed replication, honored at secret creation only |
| `Labels` | — | applied at secret creation only |
| `RetainVersions` | `0` | keep every version |
| `NodeID` | hostname + pid | lease owner |
| `ErrorHook` | — | overwritten by parsec when the store is passed to `Options.KeyRingStore` |
| `ClientOptions` | — | passed to the Secret Manager client verbatim |

## Verifying against a real project

The store's test suite runs against an in-process fake, which is fast and
hermetic but can only encode what its author believed Secret Manager
does. The compare-and-set that stops two nodes from overwriting each
other's rotations rests on `UpdateSecret` enforcing the secret's etag, so
that belief is worth checking against the real service before trusting a
fleet to it:

```bash
PARSEC_GCP_PROJECT=my-scratch-project PARSEC_GCP_CREDENTIALS=/path/to/sa.json   go test -tags gcplive -run TestLive -v ./stores/gcpsecretmanager/...
```

Each test creates its own secret and deletes it afterwards. Use a scratch
project — they write real versions to a real secret.

## Observability

| Metric | With this store |
|---|---|
| `parsec_keyring_backend{backend="external"}` | `1` |
| `parsec_keyring_version` | the secret version number this node is serving |
| `parsec_keyring_watch_up` | `1` while the poll loop is running |
| `parsec_keyring_reconcile_errors_total` | failed polls |

The backend gauge reports `external` rather than a store-specific label:
the label set is fixed so that a custom store cannot grow the metric's
cardinality. `parsec_keyring_version` is the signal that matters — alert
on nodes disagreeing about it.
