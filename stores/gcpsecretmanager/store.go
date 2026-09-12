package gcpsecretmanager

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/frankbardon/parsec/auth"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// crc32c is the checksum Secret Manager verifies payloads with.
var crc32c = crc32.MakeTable(crc32.Castagnoli)

// KeyRingStore persists the parsec KeyRing as a Secret Manager secret:
// one secret, one JSON payload, a new version per rotation. The latest
// version is the ring; the version number is the revision every node
// compares against.
//
// It satisfies auth.KeyRingStore, plus the optional halves a store shared
// across nodes needs — auth.KeyRingVersioner, auth.KeyRingErrorReporter
// and auth.KeyRingBootstrapper.
type KeyRingStore struct {
	client     *secretmanager.Client
	ownsClient bool

	secretName       string
	reconcileEvery   time.Duration
	lockTTL          time.Duration
	lockWait         time.Duration
	retainVersions   int
	nodeID           string
	replicaLocations []string
	labels           map[string]string

	// lastVersion is the secret version number this store last read or
	// wrote — the revision Save compares against.
	// auth.KeyRingVersionUnknown until the first read.
	lastVersion atomic.Int64

	// saveMu serializes writers inside this process so they queue on a
	// mutex instead of on the remote lease.
	saveMu sync.Mutex

	// secretReady records that the secret container exists, so the common
	// path does not re-create it on every write.
	secretReady atomic.Bool

	onError atomic.Pointer[func(error)]
}

var (
	_ auth.KeyRingStore         = (*KeyRingStore)(nil)
	_ auth.KeyRingVersioner     = (*KeyRingStore)(nil)
	_ auth.KeyRingErrorReporter = (*KeyRingStore)(nil)
	_ auth.KeyRingBootstrapper  = (*KeyRingStore)(nil)
)

// New builds a store and the Secret Manager client behind it, using the
// credentials named in cfg. The caller owns the store and should Close it
// at shutdown.
func New(ctx context.Context, cfg Config) (*KeyRingStore, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	client, err := secretmanager.NewClient(ctx, cfg.clientOptions()...)
	if err != nil {
		return nil, fmt.Errorf("gcpsecretmanager: build secret manager client: %w", err)
	}
	s, err := NewWithClient(cfg, client)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	s.ownsClient = true
	return s, nil
}

// NewWithClient builds a store on a client the caller already has — an
// impersonated client, one wired to an emulator, one shared with other
// Secret Manager use in the same process. Close does not close it; the
// caller keeps that responsibility.
func NewWithClient(cfg Config, client *secretmanager.Client) (*KeyRingStore, error) {
	if client == nil {
		return nil, errors.New("gcpsecretmanager: nil secret manager client")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	s := &KeyRingStore{
		client:           client,
		secretName:       fmt.Sprintf("projects/%s/secrets/%s", cfg.ProjectID, cfg.SecretID),
		reconcileEvery:   cfg.ReconcileInterval,
		lockTTL:          cfg.LockTTL,
		lockWait:         cfg.LockWait,
		retainVersions:   cfg.RetainVersions,
		nodeID:           cfg.NodeID,
		replicaLocations: cfg.ReplicaLocations,
		labels:           cfg.Labels,
	}
	s.lastVersion.Store(auth.KeyRingVersionUnknown)
	if cfg.ErrorHook != nil {
		s.SetErrorHook(cfg.ErrorHook)
	}
	return s, nil
}

// SecretName returns the fully qualified secret resource name, for logs
// and for the operator who has to grant IAM on it.
func (s *KeyRingStore) SecretName() string { return s.secretName }

// Version returns the secret version number this node last read or wrote,
// or auth.KeyRingVersionUnknown before the first read. Nodes serving the
// same ring report the same number; a node stuck on an old one is visible
// on parsec_keyring_version.
func (s *KeyRingStore) Version() int64 { return s.lastVersion.Load() }

// SetErrorHook installs the callback for non-fatal Watch errors,
// satisfying auth.KeyRingErrorReporter.
func (s *KeyRingStore) SetErrorHook(f func(error)) {
	if f == nil {
		s.onError.Store(nil)
		return
	}
	s.onError.Store(&f)
}

func (s *KeyRingStore) reportError(err error) {
	if err == nil {
		return
	}
	if f := s.onError.Load(); f != nil {
		(*f)(err)
	}
}

// Close releases the client when this store built it.
func (s *KeyRingStore) Close() error {
	if s == nil || !s.ownsClient {
		return nil
	}
	return s.client.Close()
}

// Load reads the latest secret version and records the revision it
// belongs to. Payload and version number arrive in the same response, so
// the pair cannot be mismatched.
//
// Returns (nil, os.ErrNotExist) when the secret has no versions yet — or
// does not exist at all — so the caller can decide whether to bootstrap.
func (s *KeyRingStore) Load(ctx context.Context) (*auth.KeyRing, error) {
	ring, _, err := s.load(ctx)
	return ring, err
}

func (s *KeyRingStore) load(ctx context.Context) (*auth.KeyRing, int64, error) {
	resp, err := s.client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: s.secretName + "/versions/latest",
	})
	if status.Code(err) == codes.NotFound {
		// Record "still empty" so the bootstrap Save is a compare-and-set
		// against it: two nodes starting against a fresh project must not
		// each mint a ring and have the loser sign with keys the rest of
		// the fleet rejects.
		s.lastVersion.Store(0)
		return nil, 0, os.ErrNotExist
	}
	if status.Code(err) == codes.FailedPrecondition {
		// The newest version exists but is disabled or destroyed, and
		// "latest" does not fall back to an older one. Say which secret,
		// because the fix is an operator enabling it again.
		return nil, 0, fmt.Errorf("gcpsecretmanager: latest version of %s is not enabled: %w", s.secretName, err)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("gcpsecretmanager: access %s: %w", s.secretName, err)
	}
	body := resp.GetPayload().GetData()
	if want := resp.GetPayload().GetDataCrc32C(); want != 0 {
		if got := int64(crc32.Checksum(body, crc32c)); got != want {
			return nil, 0, fmt.Errorf("gcpsecretmanager: keyring payload checksum mismatch for %s (got %d, want %d)",
				resp.GetName(), got, want)
		}
	}
	version, err := parseVersionNumber(resp.GetName())
	if err != nil {
		return nil, 0, err
	}
	ring, err := auth.DecodeKeyRing(body)
	if err != nil {
		return nil, 0, err
	}
	s.lastVersion.Store(version)
	return ring, version, nil
}

// Save appends r as a new secret version, but only if the latest version
// is still the one this store last read.
//
// Secret Manager appends unconditionally and the newest version wins for
// every reader, so an unguarded write from a node holding a stale ring
// would erase another node's rotation and report success. The write
// therefore happens under an etag-compare-and-set lease (see lock.go)
// with the revision re-checked inside it; a mismatch writes nothing and
// returns auth.ErrKeyRingConflict for the caller to reload and re-apply.
func (s *KeyRingStore) Save(ctx context.Context, r *auth.KeyRing) error {
	body, err := auth.EncodeKeyRing(r)
	if err != nil {
		return err
	}
	if len(body) > maxPayloadBytes {
		return fmt.Errorf("gcpsecretmanager: keyring snapshot is %d bytes, over Secret Manager's %d byte version limit",
			len(body), maxPayloadBytes)
	}

	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	if err := s.ensureSecret(ctx); err != nil {
		return err
	}
	expected := s.lastVersion.Load()
	release, err := s.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer release()

	current, err := s.latestVersionNumber(ctx)
	if err != nil {
		return err
	}
	if expected != auth.KeyRingVersionUnknown && current != expected {
		return auth.ErrKeyRingConflict
	}

	sum := int64(crc32.Checksum(body, crc32c))
	resp, err := s.client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent:  s.secretName,
		Payload: &secretmanagerpb.SecretPayload{Data: body, DataCrc32C: &sum},
	})
	if err != nil {
		return fmt.Errorf("gcpsecretmanager: add version to %s: %w", s.secretName, err)
	}
	written, err := parseVersionNumber(resp.GetName())
	if err != nil {
		return err
	}
	s.lastVersion.Store(written)
	s.pruneVersions(ctx, written)
	return nil
}

// Watch polls for a rotation written by another node and re-loads when
// one lands. Secret Manager offers no client-subscribable change feed —
// its rotation notifications go to Pub/Sub, which would make every parsec
// deployment provision a topic and a subscription — so the poll is the
// whole mechanism, not a backstop for one.
//
// It returns only when ctx is canceled. A failed poll is reported and
// retried on the next tick: a Watch that gave up would leave this node
// serving keys it can never update again, with one log line as the only
// symptom.
func (s *KeyRingStore) Watch(ctx context.Context, onChange func(*auth.KeyRing)) error {
	if s.reconcileEvery <= 0 {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(s.reconcileEvery)
	defer t.Stop()

	notified := s.lastVersion.Load()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			// Writes made by this node also move lastVersion; taking it
			// into account here is what stops a local rotation from
			// bouncing back as a remote change.
			if v := s.lastVersion.Load(); v > notified {
				notified = v
			}
			latest, err := s.latestVersionNumber(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.reportError(fmt.Errorf("gcpsecretmanager: keyring reconcile: %w", err))
				}
				continue
			}
			if latest == 0 || latest <= notified {
				continue
			}
			ring, version, err := s.load(ctx)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) && ctx.Err() == nil {
					s.reportError(fmt.Errorf("gcpsecretmanager: keyring reconcile: %w", err))
				}
				continue
			}
			if version <= notified {
				continue
			}
			notified = version
			onChange(ring)
		}
	}
}

// Ensure returns the persisted ring, minting one when the secret is empty.
// The bool reports whether this call bootstrapped.
//
// The bootstrap write goes through Save, so it is a compare-and-set
// against "still no versions": the node that loses the race adopts the
// winner's ring instead of overwriting it. Both must sign with the same
// keys, or every token minted by one is rejected by the other.
func (s *KeyRingStore) Ensure(ctx context.Context) (*auth.KeyRing, bool, error) {
	ring, err := s.Load(ctx)
	if err == nil {
		return ring, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	ring = auth.NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		return nil, false, err
	}
	if err := s.Save(ctx, ring); err != nil {
		if !errors.Is(err, auth.ErrKeyRingConflict) {
			return nil, false, err
		}
		ring, err = s.Load(ctx)
		if err != nil {
			return nil, false, err
		}
		return ring, false, nil
	}
	return ring, true, nil
}

// ensureSecret creates the secret container if it is missing. Creating it
// is a compare-and-set of its own: the node that loses sees ALREADY_EXISTS
// and carries on, because the container holds no key material — the
// versions do.
func (s *KeyRingStore) ensureSecret(ctx context.Context) error {
	if s.secretReady.Load() {
		return nil
	}
	if _, err := s.client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: s.secretName}); err == nil {
		s.secretReady.Store(true)
		return nil
	} else if status.Code(err) != codes.NotFound {
		return fmt.Errorf("gcpsecretmanager: get secret %s: %w", s.secretName, err)
	}

	parent, secretID, err := splitSecretName(s.secretName)
	if err != nil {
		return err
	}
	_, err = s.client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   parent,
		SecretId: secretID,
		Secret: &secretmanagerpb.Secret{
			Replication: s.replication(),
			Labels:      s.labels,
		},
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("gcpsecretmanager: create secret %s: %w", s.secretName, err)
	}
	s.secretReady.Store(true)
	return nil
}

// replication builds the policy for a secret this node creates. Key
// material is the one thing in parsec with a data-residency story, so
// pinned locations are honored when given.
func (s *KeyRingStore) replication() *secretmanagerpb.Replication {
	if len(s.replicaLocations) == 0 {
		return &secretmanagerpb.Replication{
			Replication: &secretmanagerpb.Replication_Automatic_{
				Automatic: &secretmanagerpb.Replication_Automatic{},
			},
		}
	}
	replicas := make([]*secretmanagerpb.Replication_UserManaged_Replica, 0, len(s.replicaLocations))
	for _, loc := range s.replicaLocations {
		replicas = append(replicas, &secretmanagerpb.Replication_UserManaged_Replica{Location: loc})
	}
	return &secretmanagerpb.Replication{
		Replication: &secretmanagerpb.Replication_UserManaged_{
			UserManaged: &secretmanagerpb.Replication_UserManaged{Replicas: replicas},
		},
	}
}

// latestVersionNumber returns the newest version number, or 0 when the
// secret has none. It reads metadata rather than the payload: the poll in
// Watch runs on every node on every tick, and there is no reason to
// decrypt the ring to learn whether it changed.
func (s *KeyRingStore) latestVersionNumber(ctx context.Context) (int64, error) {
	v, err := s.client.GetSecretVersion(ctx, &secretmanagerpb.GetSecretVersionRequest{
		Name: s.secretName + "/versions/latest",
	})
	if status.Code(err) == codes.NotFound {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("gcpsecretmanager: get latest version of %s: %w", s.secretName, err)
	}
	return parseVersionNumber(v.GetName())
}

// pruneVersions destroys superseded versions once RetainVersions is set,
// keeping that many of the most recent. Best effort: the ring is already
// written, and a failure here is housekeeping, not a rotation that did
// not happen.
func (s *KeyRingStore) pruneVersions(ctx context.Context, latest int64) {
	if s.retainVersions <= 0 {
		return
	}
	cutoff := latest - int64(s.retainVersions)
	if cutoff < 1 {
		return
	}
	it := s.client.ListSecretVersions(ctx, &secretmanagerpb.ListSecretVersionsRequest{Parent: s.secretName})
	for {
		v, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return
		}
		if err != nil {
			s.reportError(fmt.Errorf("gcpsecretmanager: list versions of %s: %w", s.secretName, err))
			return
		}
		if v.GetState() != secretmanagerpb.SecretVersion_ENABLED {
			continue
		}
		n, err := parseVersionNumber(v.GetName())
		if err != nil || n > cutoff {
			continue
		}
		if _, err := s.client.DestroySecretVersion(ctx, &secretmanagerpb.DestroySecretVersionRequest{Name: v.GetName()}); err != nil {
			s.reportError(fmt.Errorf("gcpsecretmanager: destroy %s: %w", v.GetName(), err))
		}
	}
}

// parseVersionNumber pulls the numeric version out of a resource name
// like projects/p/secrets/s/versions/7. Responses resolve the "latest"
// alias to a number, which is what makes the alias usable as a revision.
func parseVersionNumber(name string) (int64, error) {
	i := strings.LastIndex(name, "/versions/")
	if i < 0 {
		return 0, fmt.Errorf("gcpsecretmanager: %q is not a secret version name", name)
	}
	n, err := strconv.ParseInt(name[i+len("/versions/"):], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("gcpsecretmanager: %q has no numeric version: %w", name, err)
	}
	return n, nil
}

// splitSecretName turns projects/p/secrets/s into its create-request parts.
func splitSecretName(name string) (parent, secretID string, err error) {
	i := strings.LastIndex(name, "/secrets/")
	if i < 0 {
		return "", "", fmt.Errorf("gcpsecretmanager: %q is not a secret name", name)
	}
	return name[:i], name[i+len("/secrets/"):], nil
}
