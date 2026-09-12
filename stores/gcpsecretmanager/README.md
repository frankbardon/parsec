# parsec keyring store — Google Secret Manager

A third `auth.KeyRingStore` for [parsec](https://github.com/frankbardon/parsec),
keeping the JWT signing keys in Google Cloud Secret Manager instead of
`keyring.json` or Redis.

It is a **separate Go module** on purpose: the Google Cloud SDK pulls in
grpc-go and the GCP auth stack, and a deployment that does not use Secret
Manager should not carry them.

```bash
go get github.com/frankbardon/parsec/stores/gcpsecretmanager
```

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

Omit `CredentialsFile` / `CredentialsJSON` to use Application Default
Credentials — the right answer under workload identity on GKE or Cloud
Run, where there is no key file to mount. When either is set it must be a
**service account** key: the credential type is pinned rather than sniffed
from the file.

## How it stores the ring

One secret, one JSON payload, a version per rotation:

| Resource | Holds |
|---|---|
| `projects/<project>/secrets/<secret-id>` | the keyring |
| `.../versions/N` | revision N of the ring |
| annotation `parsec-keyring-lock` | the short-lived write lease |

The version number is the revision every node compares against, and it is
what `parsec_keyring_version` reports.

## Concurrent rotation

Secret Manager appends versions unconditionally and the newest one wins
for every reader, so two nodes rotating at once would both append and the
second would erase the first — successfully, as far as its caller could
tell. The store closes that window:

1. `Save` takes a write lease, written as a secret annotation with the
   secret's etag as the precondition. `UpdateSecret` honors that etag as a
   compare-and-set, which makes it the one atomic primitive available.
2. Under the lease it re-reads the latest version number, and refuses with
   `auth.ErrKeyRingConflict` when it is no longer the revision this node
   loaded. parsec reloads, re-applies the mutation and retries.
3. It appends the new version, then drops the lease.

A node that dies mid-write does not wedge the fleet: the lease carries a
deadline (`Config.LockTTL`, default 30s) and the next writer takes it over.
Stealing it is itself an etag compare-and-set, and the revision check still
runs underneath, so an expired lease never costs correctness.

The same check covers bootstrap. Two nodes starting against an empty
project would otherwise each mint a ring, and the loser would sign tokens
the rest of the fleet rejects; instead the losing write is a conflict and
that node adopts the winner's ring.

## IAM

Grant on the one secret (`roles/secretmanager.admin` covers all of it):

| Permission | Why |
|---|---|
| `secretmanager.versions.access` | read the ring |
| `secretmanager.versions.add` | write a rotation |
| `secretmanager.versions.get` | the reconcile poll |
| `secretmanager.secrets.get` | read the write lease |
| `secretmanager.secrets.update` | take and release the write lease |
| `secretmanager.secrets.create` | only to create the secret on first boot |
| `secretmanager.versions.list`, `.destroy` | only with `RetainVersions` set |

Creating the secret out of band and leaving `secretmanager.secrets.create`
off the binding is fine — the store uses the existing secret.

## Configuration

| Field | Default | Notes |
|---|---|---|
| `ProjectID` | — | required; never inferred from the credentials |
| `SecretID` | `parsec-keyring` | |
| `CredentialsFile` / `CredentialsJSON` | ADC | service account key; mutually exclusive |
| `ReconcileInterval` | `30s` | rotation latency across the fleet; negative disables the poll |
| `LockTTL` / `LockWait` | `30s` / `10s` | write-lease lifetime, and how long `Save` waits on somebody else's |
| `ReplicaLocations` | automatic | user-managed replication, at secret creation only |
| `RetainVersions` | `0` (keep all) | destroys superseded versions after a write |
| `NodeID` | hostname + pid | lease owner |
| `ClientOptions` | — | passed to the Secret Manager client verbatim |

## Development

The module pins a published parsec version, so `go get` resolves it the way
a consumer's build does. The repo ships a `go.work` that overrides the pin
with the working tree, so `go test ./...` here exercises your local parsec
changes with no extra setup.

To build the way a consumer does — against the pinned, published parsec —
turn the workspace off:

```bash
GOWORK=off go build ./... && GOWORK=off go test ./...
```

CI runs both: `make test` through the workspace, and a `submodule-pin` job
with `GOWORK=off` that also rejects a `replace` directive, which consumers
ignore and which would make a tagged module ungettable.

```bash
go test ./...      # runs against an in-process fake Secret Manager
```

The fake implements the three behaviors the store's correctness rests on:
etag-checked `UpdateSecret`, append-only versions, and a `latest` alias
that resolves to the most recently created version whatever its state. It
is still a fake — it encodes what its author believed the API does. The
`gcplive` tests check those beliefs against the real service, above all
that a stale etag is actually refused:

```bash
PARSEC_GCP_PROJECT=my-scratch-project PARSEC_GCP_CREDENTIALS=/path/to/sa.json   go test -tags gcplive -run TestLive -v ./...
```

They create a uniquely named secret per test and delete it on the way
out, but they do write to a real project — point them at a scratch one.
They are excluded from every normal build and from CI.
