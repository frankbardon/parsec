package gcpsecretmanager

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"google.golang.org/api/option"
)

const (
	// DefaultSecretID is the secret the keyring lives in when Config
	// leaves SecretID empty.
	DefaultSecretID = "parsec-keyring"

	// DefaultReconcileInterval is how often Watch re-reads the secret's
	// latest version number. Secret Manager has no change feed a client
	// can subscribe to, so this poll is the only thing bounding how long
	// a node can serve a ring somebody else already rotated.
	DefaultReconcileInterval = 30 * time.Second

	// DefaultLockTTL is how long a write lease is honored by other nodes.
	// It bounds how long a node that died mid-rotation can block the
	// fleet, so it is deliberately short: the work it covers is two RPCs.
	DefaultLockTTL = 30 * time.Second

	// DefaultLockWait is how long Save waits for a lease held by another
	// node before giving up. Longer than a healthy write, shorter than
	// the 5s deadline parsec puts on its own Save calls would allow — the
	// caller's context still wins when it is tighter.
	DefaultLockWait = 10 * time.Second
)

// maxPayloadBytes is Secret Manager's hard limit on a version payload.
// A keyring that outgrows it cannot be persisted at all, so Save says so
// in those terms rather than surfacing a raw INVALID_ARGUMENT.
const maxPayloadBytes = 64 * 1024

// secretIDPattern is Secret Manager's own rule for a secret id.
var secretIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

// Config describes which Secret Manager secret holds the keyring and how
// this node authenticates to reach it.
//
// Credentials are resolved in this order: CredentialsJSON, then
// CredentialsFile, then Application Default Credentials — which is what a
// workload-identity deployment on GKE or Cloud Run wants, since it has no
// key file to hand over. The service account behind whichever wins needs,
// on this one secret:
//
//	secretmanager.versions.access   (read the ring)
//	secretmanager.versions.add      (write a rotation)
//	secretmanager.versions.get      (the Watch poll)
//	secretmanager.secrets.get       (read the write lease)
//	secretmanager.secrets.update    (take and release the write lease)
//
// plus secretmanager.secrets.create if the secret has not been created
// out of band, secretmanager.versions.list and
// secretmanager.versions.destroy if RetainVersions is set. The predefined
// roles/secretmanager.admin covers all of them; a least-privilege
// deployment binds the permissions above directly on the secret.
type Config struct {
	// ProjectID is the Google Cloud project holding the secret. Required
	// — the project is not inferred from the credentials, because a
	// service account routinely has access to more than one and guessing
	// would pick the wrong keyring silently.
	ProjectID string

	// SecretID is the secret name inside the project. Defaults to
	// DefaultSecretID. The secret holds the whole ring as one JSON
	// payload, versioned: every rotation adds a version, and the latest
	// version is the ring.
	SecretID string

	// CredentialsFile is the path to a service account key JSON file. The
	// file is read by the Google auth library, not by parsec, and is
	// required to be a service account key: any other credential
	// configuration in that file is rejected rather than honored.
	CredentialsFile string

	// CredentialsJSON is a service account key as raw JSON, for
	// deployments that mount the key as an env var or pull it from
	// another secret store rather than a file. Mutually exclusive with
	// CredentialsFile, and held to the same service-account-only rule.
	CredentialsJSON []byte

	// ReconcileInterval is how often Watch polls for a rotation written
	// by another node. Zero picks DefaultReconcileInterval; negative
	// disables the poll, which leaves this node's ring frozen until an
	// explicit reload — only sane for a single-writer deployment.
	ReconcileInterval time.Duration

	// LockTTL is how long this node's write lease stays valid, and
	// LockWait how long Save waits on somebody else's. Zero picks the
	// defaults.
	LockTTL  time.Duration
	LockWait time.Duration

	// ReplicaLocations pins the secret to specific regions when this node
	// is the one that creates it (user-managed replication). Empty means
	// automatic replication. Ignored when the secret already exists — the
	// replication policy is fixed at creation.
	ReplicaLocations []string

	// Labels are applied to the secret on creation. Ignored for a secret
	// that already exists.
	Labels map[string]string

	// RetainVersions, when > 0, destroys superseded versions after a
	// successful write, keeping this many of the most recent. Zero keeps
	// every version forever, which is the safe default: a destroyed
	// version is unrecoverable, and old rings are the only way back from
	// a bad rotation.
	RetainVersions int

	// NodeID identifies this node in the write lease. Defaults to
	// hostname + pid. It only has to be unique within the fleet.
	NodeID string

	// ErrorHook receives non-fatal Watch errors — a failed poll, a
	// transient RPC error. Watch keeps running either way. parsec
	// installs its own hook (auth.KeyRingErrorReporter) when the store is
	// passed via parsec.Options.KeyRingStore, overwriting this one.
	ErrorHook func(error)

	// ClientOptions are passed through to the Secret Manager client
	// verbatim — a custom endpoint, a quota project, an impersonated
	// service account. Credentials set here win over CredentialsFile and
	// CredentialsJSON, since they are applied last.
	ClientOptions []option.ClientOption
}

// validate fills in defaults and rejects a Config that cannot work.
func (c *Config) validate() error {
	if c.ProjectID == "" {
		return errors.New("gcpsecretmanager: ProjectID is required")
	}
	if c.SecretID == "" {
		c.SecretID = DefaultSecretID
	}
	if !secretIDPattern.MatchString(c.SecretID) {
		return fmt.Errorf("gcpsecretmanager: SecretID %q must match %s", c.SecretID, secretIDPattern)
	}
	if c.CredentialsFile != "" && len(c.CredentialsJSON) > 0 {
		return errors.New("gcpsecretmanager: set CredentialsFile or CredentialsJSON, not both")
	}
	if c.RetainVersions < 0 {
		return fmt.Errorf("gcpsecretmanager: RetainVersions %d must not be negative", c.RetainVersions)
	}
	if c.ReconcileInterval == 0 {
		c.ReconcileInterval = DefaultReconcileInterval
	}
	if c.LockTTL <= 0 {
		c.LockTTL = DefaultLockTTL
	}
	if c.LockWait <= 0 {
		c.LockWait = DefaultLockWait
	}
	if c.NodeID == "" {
		c.NodeID = defaultNodeID()
	}
	return nil
}

// clientOptions turns the credential fields into client options. The
// caller's own ClientOptions are appended last so they can override.
func (c *Config) clientOptions() []option.ClientOption {
	var opts []option.ClientOption
	switch {
	case len(c.CredentialsJSON) > 0:
		opts = append(opts, option.WithAuthCredentialsJSON(option.ServiceAccount, c.CredentialsJSON))
	case c.CredentialsFile != "":
		// Handing the path to the Google auth library keeps the file read
		// out of parsec: library packages here do not touch the OS
		// filesystem directly.
		//
		// The credential type is pinned to ServiceAccount rather than
		// sniffed from the JSON. A keyring is the crown jewels; a
		// credential file swapped for an external-account configuration
		// would otherwise send this node's token requests to whatever URL
		// that file names, and it would look like it worked.
		opts = append(opts, option.WithAuthCredentialsFile(option.ServiceAccount, c.CredentialsFile))
	}
	return append(opts, c.ClientOptions...)
}

// defaultNodeID names this process in a write lease. Only uniqueness
// within the fleet matters, and only for the duration of one write.
func defaultNodeID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return sanitizeNodeID(host) + "-" + strconv.Itoa(os.Getpid())
}

// sanitizeNodeID strips what an annotation value cannot carry. A node id
// that fails the API's validation would make every write fail, which is a
// silly way to lose a fleet to an unusual hostname.
func sanitizeNodeID(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	if len(out) > 64 {
		out = out[:64]
	}
	if len(out) == 0 {
		return "node"
	}
	return string(out)
}
