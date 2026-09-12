// Package gcpsecretmanager stores the parsec keyring in Google Cloud
// Secret Manager.
//
// It is the third auth.KeyRingStore implementation, after the file store
// (single node) and the Redis store (a fleet sharing one Redis). It suits
// a deployment that already treats Secret Manager as the system of record
// for secrets: the signing keys land under the same IAM, audit log,
// CMEK and residency policy as everything else, and no node needs a
// writable disk or a Redis that is durable enough to hold key material.
//
// It lives in its own module so that the Google Cloud SDK — grpc-go, the
// GCP auth stack, and their transitive tree — stays out of the parsec
// module's dependency graph for everyone who does not use it.
//
// # Layout
//
// One secret holds the whole ring as a single JSON payload, and every
// rotation adds a version:
//
//	projects/<project>/secrets/<secret-id>            the ring
//	projects/<project>/secrets/<secret-id>/versions/N revision N
//
// The version number is the revision. Load pairs the snapshot it returns
// with the version it came from, and Save refuses to write when the
// latest version is no longer that one — see Save for why that takes a
// lease rather than an etag alone.
//
// # Wiring it up
//
//	store, err := gcpsecretmanager.New(ctx, gcpsecretmanager.Config{
//	        ProjectID:       "my-project",
//	        SecretID:        "parsec-keyring",
//	        CredentialsFile: "/var/run/secrets/parsec-sa.json",
//	})
//	if err != nil {
//	        return err
//	}
//	defer store.Close()
//
//	p, err := parsec.New(parsec.Options{KeyRingStore: store})
//
// parsec.New bootstraps the ring through the store (minting one if the
// secret is empty), reloads it when another node rotates, and persists
// every `parsec keys` mutation back to it. Omit CredentialsFile to use
// Application Default Credentials, which is what workload identity on
// GKE or Cloud Run provides.
//
// # Operational notes
//
//   - Secret Manager holds the only copy of the signing keys. Losing the
//     secret is losing every token minted under it; back it up the same
//     way as any other secret of record.
//   - Rotation latency across the fleet is Config.ReconcileInterval: the
//     API has no change feed a client can subscribe to, so the store
//     polls the latest version number.
//   - Versions accumulate, one per rotation, and are kept forever unless
//     Config.RetainVersions says otherwise. That is deliberate: a
//     destroyed version is unrecoverable, and the previous ring is the
//     only way back from a bad rotation.
//   - The store reports "external" on the parsec_keyring_backend gauge,
//     and its version number on parsec_keyring_version — nodes that
//     disagree there are serving different rings.
package gcpsecretmanager
