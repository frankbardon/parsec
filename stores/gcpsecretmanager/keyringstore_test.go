package gcpsecretmanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/frankbardon/parsec/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testSecretName = "projects/test-project/secrets/parsec-keyring"

func newTestStore(t *testing.T, client *secretmanager.Client, tweak func(*Config)) *KeyRingStore {
	t.Helper()
	cfg := Config{
		ProjectID:         "test-project",
		ReconcileInterval: 20 * time.Millisecond,
		LockTTL:           2 * time.Second,
		LockWait:          200 * time.Millisecond,
		NodeID:            "node-" + strconv.Itoa(int(time.Now().UnixNano()%1e6)),
	}
	if tweak != nil {
		tweak(&cfg)
	}
	s, err := NewWithClient(cfg, client)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	return s
}

func hasKey(ring *auth.KeyRing, id string) bool {
	_, err := ring.Get(id)
	return err == nil
}

func TestLoadEmptyReportsNotExistAndRecordsEmptyRevision(t *testing.T) {
	_, client := startFake(t)
	s := newTestStore(t, client, nil)

	if s.Version() != auth.KeyRingVersionUnknown {
		t.Fatalf("fresh store reports version %d, want unknown", s.Version())
	}
	_, err := s.Load(context.Background())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load on empty project: %v, want os.ErrNotExist", err)
	}
	// The revision has to be recorded even though there was nothing to
	// read: it is what makes the bootstrap write a compare-and-set
	// against "still empty" rather than an unconditional first write.
	if got := s.Version(); got != 0 {
		t.Fatalf("after empty Load, Version() = %d, want 0", got)
	}
}

func TestEnsureBootstrapsThenAdoptsOnReload(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	first := newTestStore(t, client, nil)
	ring, bootstrapped, err := first.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !bootstrapped {
		t.Fatal("first Ensure did not report a bootstrap")
	}
	if ring.ActiveID() == "" {
		t.Fatal("bootstrapped ring has no active key")
	}
	if got := fake.versionCount(testSecretName); got != 1 {
		t.Fatalf("bootstrap wrote %d versions, want 1", got)
	}
	if got := first.Version(); got != 1 {
		t.Fatalf("Version() = %d after bootstrap, want 1", got)
	}

	second := newTestStore(t, client, nil)
	adopted, bootstrapped, err := second.Ensure(ctx)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if bootstrapped {
		t.Fatal("second Ensure bootstrapped a ring instead of adopting the stored one")
	}
	if adopted.ActiveID() != ring.ActiveID() {
		t.Fatalf("second node adopted active key %q, want %q", adopted.ActiveID(), ring.ActiveID())
	}
}

// TestSaveRejectsWriteFromStaleRing is the point of the whole store.
// Secret Manager appends versions unconditionally and the newest wins, so
// without the compare-and-set the second node's write would land and
// erase the first node's freshly promoted key while reporting success.
func TestSaveRejectsWriteFromStaleRing(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	a := newTestStore(t, client, nil)
	b := newTestStore(t, client, nil)

	ringA, _, err := a.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ringB, err := b.Load(ctx)
	if err != nil {
		t.Fatalf("Load on second node: %v", err)
	}

	keyA, err := ringA.Generate()
	if err != nil {
		t.Fatalf("generate on A: %v", err)
	}
	if err := a.Save(ctx, ringA); err != nil {
		t.Fatalf("A Save: %v", err)
	}

	keyB, err := ringB.Generate()
	if err != nil {
		t.Fatalf("generate on B: %v", err)
	}
	if err := b.Save(ctx, ringB); !errors.Is(err, auth.ErrKeyRingConflict) {
		t.Fatalf("B Save from a stale ring: %v, want auth.ErrKeyRingConflict", err)
	}
	if got := fake.versionCount(testSecretName); got != 2 {
		t.Fatalf("conflicting Save wrote a version: %d versions, want 2", got)
	}

	latest, err := a.Load(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !hasKey(latest, keyA.ID) {
		t.Fatalf("A's rotation was erased: key %q missing from the stored ring", keyA.ID)
	}
	if hasKey(latest, keyB.ID) {
		t.Fatalf("B's stale write landed: key %q is in the stored ring", keyB.ID)
	}

	// The documented recovery: reload, re-apply, save. parsec.mutateKeys
	// does exactly this on auth.ErrKeyRingConflict.
	fresh, err := b.Load(ctx)
	if err != nil {
		t.Fatalf("B reload: %v", err)
	}
	if _, err := fresh.Generate(); err != nil {
		t.Fatalf("B regenerate: %v", err)
	}
	if err := b.Save(ctx, fresh); err != nil {
		t.Fatalf("B Save after reload: %v", err)
	}
	if got := fake.versionCount(testSecretName); got != 3 {
		t.Fatalf("after retry: %d versions, want 3", got)
	}
}

// TestConcurrentBootstrapAdoptsOneRing covers the other half of the
// compare-and-set: two nodes booting against a fresh project. Without the
// "still empty" check they each mint a ring, and the loser signs tokens
// with keys the rest of the fleet rejects.
func TestConcurrentBootstrapAdoptsOneRing(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	const nodes = 4
	rings := make([]*auth.KeyRing, nodes)
	errs := make([]error, nodes)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range nodes {
		s := newTestStore(t, client, func(c *Config) { c.LockWait = 5 * time.Second })
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rings[i], _, errs[i] = s.Ensure(ctx)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("node %d Ensure: %v", i, err)
		}
	}
	if got := fake.versionCount(testSecretName); got != 1 {
		t.Fatalf("concurrent bootstrap wrote %d versions, want 1", got)
	}
	for i, r := range rings {
		if r.ActiveID() != rings[0].ActiveID() {
			t.Fatalf("node %d signs with %q, node 0 with %q — the fleet disagrees on the active key",
				i, r.ActiveID(), rings[0].ActiveID())
		}
	}
}

func TestSaveWaitsOutAndStealsTheWriteLease(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Another node is mid-write and its lease is live.
	held := lease{owner: "other-node", expires: time.Now().Add(time.Minute)}
	fake.setAnnotation(testSecretName, lockAnnotation, held.encode())

	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	err = s.Save(ctx, ring)
	if err == nil {
		t.Fatal("Save wrote through a lease held by another node")
	}
	if !strings.Contains(err.Error(), "other-node") {
		t.Fatalf("Save error %v does not name the lease holder", err)
	}
	if got := fake.versionCount(testSecretName); got != 1 {
		t.Fatalf("blocked Save wrote a version: %d versions, want 1", got)
	}

	// A node that died mid-write must not block rotation forever: once
	// its lease has expired, the next writer takes it over.
	expired := lease{owner: "other-node", expires: time.Now().Add(-time.Minute)}
	fake.setAnnotation(testSecretName, lockAnnotation, expired.encode())
	if err := s.Save(ctx, ring); err != nil {
		t.Fatalf("Save with an expired lease in place: %v", err)
	}
	if got := fake.versionCount(testSecretName); got != 2 {
		t.Fatalf("after stealing an expired lease: %d versions, want 2", got)
	}
}

func TestSaveReleasesTheLeaseOnSuccess(t *testing.T) {
	_, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := s.Save(ctx, ring); err != nil {
		t.Fatalf("Save: %v", err)
	}

	sec, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: testSecretName})
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if v, ok := sec.GetAnnotations()[lockAnnotation]; ok {
		t.Fatalf("lease annotation %q outlived the write", v)
	}
}

func TestWatchDeliversRemoteRotationAndIgnoresOwnWrite(t *testing.T) {
	_, client := startFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := newTestStore(t, client, nil)
	writer := newTestStore(t, client, nil)

	ring, _, err := watcher.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	changes := make(chan *auth.KeyRing, 8)
	done := make(chan error, 1)
	go func() { done <- watcher.Watch(ctx, func(r *auth.KeyRing) { changes <- r }) }()

	// The watcher's own write must not come back as a remote change.
	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := watcher.Save(ctx, ring); err != nil {
		t.Fatalf("watcher Save: %v", err)
	}
	select {
	case r := <-changes:
		t.Fatalf("watch fired for this node's own write (active %q)", r.ActiveID())
	case <-time.After(150 * time.Millisecond):
	}

	remote, err := writer.Load(ctx)
	if err != nil {
		t.Fatalf("writer Load: %v", err)
	}
	rotated, err := remote.Generate()
	if err != nil {
		t.Fatalf("writer generate: %v", err)
	}
	if err := remote.Promote(rotated.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := writer.Save(ctx, remote); err != nil {
		t.Fatalf("writer Save: %v", err)
	}

	select {
	case got := <-changes:
		if got.ActiveID() != rotated.ID {
			t.Fatalf("watch delivered active key %q, want %q", got.ActiveID(), rotated.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch never delivered the remote rotation")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch returned %v on cancel, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return after context cancel")
	}
}

// TestWatchSurvivesPollFailures asserts the failure mode the contract
// exists for: a Watch that returns on a transport error leaves this node
// serving keys it can never update again, with one log line as the only
// symptom.
func TestWatchSurvivesPollFailures(t *testing.T) {
	fake, client := startFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := newTestStore(t, client, nil)
	writer := newTestStore(t, client, nil)
	if _, _, err := watcher.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	var mu sync.Mutex
	var hookErrs int
	watcher.SetErrorHook(func(error) {
		mu.Lock()
		hookErrs++
		mu.Unlock()
	})

	var polls int
	fake.setFaults(func(method string) error {
		if method != "GetSecretVersion" {
			return nil
		}
		polls++
		if polls <= 3 {
			return status.Error(codes.Unavailable, "secret manager is having a moment")
		}
		return nil
	})

	changes := make(chan *auth.KeyRing, 4)
	done := make(chan error, 1)
	go func() { done <- watcher.Watch(ctx, func(r *auth.KeyRing) { changes <- r }) }()

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		seen := hookErrs
		mu.Unlock()
		if seen >= 3 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Watch returned %v while polls were failing; it must keep running", err)
		case <-deadline:
			t.Fatalf("only %d poll errors reported, want 3", seen)
		case <-time.After(10 * time.Millisecond):
		}
	}

	fake.setFaults(nil)
	remote, err := writer.Load(ctx)
	if err != nil {
		t.Fatalf("writer Load: %v", err)
	}
	rotated, err := remote.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := remote.Promote(rotated.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := writer.Save(ctx, remote); err != nil {
		t.Fatalf("writer Save: %v", err)
	}

	select {
	case got := <-changes:
		if got.ActiveID() != rotated.ID {
			t.Fatalf("watch delivered %q after recovering, want %q", got.ActiveID(), rotated.ID)
		}
	case err := <-done:
		t.Fatalf("Watch returned %v instead of recovering", err)
	case <-time.After(3 * time.Second):
		t.Fatal("watch never recovered from the failed polls")
	}
}

func TestWatchWithoutReconcileWaitsForCancel(t *testing.T) {
	_, client := startFake(t)
	ctx, cancel := context.WithCancel(context.Background())

	s := newTestStore(t, client, func(c *Config) { c.ReconcileInterval = -1 })
	done := make(chan error, 1)
	go func() { done <- s.Watch(ctx, func(*auth.KeyRing) {}) }()

	select {
	case err := <-done:
		t.Fatalf("Watch returned %v before cancel", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return after cancel")
	}
}

func TestLoadRejectsCorruptedPayload(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	if _, _, err := s.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Flip a byte behind the store's back, leaving the stored checksum
	// describing the original payload.
	fake.mu.Lock()
	fake.secrets[testSecretName].versions[0].data[0] ^= 0xff
	fake.mu.Unlock()

	_, err := s.Load(ctx)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Load of a corrupted payload: %v, want a checksum mismatch", err)
	}
}

func TestLoadReportsDisabledLatestVersion(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	if _, _, err := s.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	fake.mu.Lock()
	fake.secrets[testSecretName].versions[0].state = secretmanagerpb.SecretVersion_DISABLED
	fake.mu.Unlock()

	_, err := s.Load(ctx)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load with a disabled latest version: %v, want a descriptive error", err)
	}
	if !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("error %v does not explain that the version is not enabled", err)
	}
}

func TestRetainVersionsDestroysSuperseded(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, func(c *Config) { c.RetainVersions = 2 })
	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for range 3 {
		if _, err := ring.Generate(); err != nil {
			t.Fatalf("generate: %v", err)
		}
		if err := s.Save(ctx, ring); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	if got := fake.versionCount(testSecretName); got != 4 {
		t.Fatalf("%d versions, want 4", got)
	}
	for _, n := range []int{1, 2} {
		if st := fake.versionState(testSecretName, n); st != secretmanagerpb.SecretVersion_DESTROYED {
			t.Fatalf("version %d is %s, want DESTROYED", n, st)
		}
	}
	for _, n := range []int{3, 4} {
		if st := fake.versionState(testSecretName, n); st != secretmanagerpb.SecretVersion_ENABLED {
			t.Fatalf("version %d is %s, want ENABLED", n, st)
		}
	}
	if _, err := s.Load(ctx); err != nil {
		t.Fatalf("Load after pruning: %v", err)
	}
}

func TestSaveRejectsOversizePayload(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for {
		if _, err := ring.Generate(); err != nil {
			t.Fatalf("generate: %v", err)
		}
		body, err := auth.EncodeKeyRing(ring)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if len(body) > maxPayloadBytes {
			break
		}
	}

	err = s.Save(ctx, ring)
	if err == nil || !strings.Contains(err.Error(), "version limit") {
		t.Fatalf("Save of an oversize ring: %v, want a payload-limit error", err)
	}
	if got := fake.versionCount(testSecretName); got != 1 {
		t.Fatalf("oversize Save wrote a version: %d versions, want 1", got)
	}
}

func TestEnsureUsesExistingSecretWithoutCreating(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	if _, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent:   "projects/test-project",
		SecretId: "parsec-keyring",
		Secret: &secretmanagerpb.Secret{Replication: &secretmanagerpb.Replication{
			Replication: &secretmanagerpb.Replication_Automatic_{Automatic: &secretmanagerpb.Replication_Automatic{}},
		}},
	}); err != nil {
		t.Fatalf("pre-create secret: %v", err)
	}
	before := fake.callCount("CreateSecret")

	s := newTestStore(t, client, nil)
	if _, bootstrapped, err := s.Ensure(ctx); err != nil || !bootstrapped {
		t.Fatalf("Ensure on an empty pre-created secret: bootstrapped=%v err=%v", bootstrapped, err)
	}
	if got := fake.callCount("CreateSecret") - before; got != 0 {
		t.Fatalf("store called CreateSecret %d times for a secret that already exists", got)
	}
}

func TestUserManagedReplicationIsRequestedWhenPinned(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, func(c *Config) {
		c.ReplicaLocations = []string{"europe-west1", "europe-west4"}
		c.Labels = map[string]string{"component": "parsec"}
	})
	if _, _, err := s.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	sec := fake.secrets[testSecretName]
	um := sec.pb.GetReplication().GetUserManaged()
	if um == nil {
		t.Fatalf("replication is %v, want user-managed", sec.pb.GetReplication())
	}
	if len(um.GetReplicas()) != 2 || um.GetReplicas()[0].GetLocation() != "europe-west1" {
		t.Fatalf("replicas = %v, want the two pinned locations", um.GetReplicas())
	}
	if sec.pb.GetLabels()["component"] != "parsec" {
		t.Fatalf("labels = %v, want the configured label", sec.pb.GetLabels())
	}
}

func TestSecretNameAndCloseOnBorrowedClient(t *testing.T) {
	_, client := startFake(t)
	s := newTestStore(t, client, func(c *Config) { c.SecretID = "custom-ring" })
	if got, want := s.SecretName(), "projects/test-project/secrets/custom-ring"; got != want {
		t.Fatalf("SecretName() = %q, want %q", got, want)
	}
	// The store did not build this client, so Close must leave it usable.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Load(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("client unusable after Close on a borrowed client: %v", err)
	}
}

// TestAcquireLockRetriesOnEtagMismatch drives the path the real API takes
// when two nodes reach for the lease at once. The store must read the
// contention code as "re-read and try again"; treating it as fatal turns
// a retryable race into a failed rotation, and pinning it to one of the
// two codes Google's docs have used means that failure only shows up in
// production.
func TestAcquireLockRetriesOnEtagMismatch(t *testing.T) {
	for _, code := range []codes.Code{codes.FailedPrecondition, codes.Aborted} {
		t.Run(code.String(), func(t *testing.T) {
			fake, client := startFake(t)
			fake.setEtagMismatchCode(code)
			ctx := context.Background()

			s := newTestStore(t, client, func(c *Config) { c.LockWait = 5 * time.Second })
			ring, _, err := s.Ensure(ctx)
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}

			// The first attempt to take the lease loses the etag race, as
			// it would against a node that updated the secret in between.
			var raced bool
			fake.setFaults(func(method string) error {
				if method == "UpdateSecret" && !raced {
					raced = true
					fake.bumpEtagLocked(testSecretName)
				}
				return nil
			})

			key, err := ring.Generate()
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if err := s.Save(ctx, ring); err != nil {
				t.Fatalf("Save through an etag race (%s): %v", code, err)
			}
			if !raced {
				t.Fatal("the staged etag race never happened; the test proved nothing")
			}
			if got := fake.versionCount(testSecretName); got != 2 {
				t.Fatalf("%d versions after the retry, want 2", got)
			}
			stored, err := s.Load(ctx)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !hasKey(stored, key.ID) {
				t.Fatalf("key %q missing after a Save that had to retry", key.ID)
			}
		})
	}
}

// TestSaveWithoutLoadIsUnconditional pins the documented escape hatch: a
// store that has never read has no baseline to compare against, so its
// first write is not a compare-and-set. Every caller inside parsec loads
// at boot, so this is reachable only by an embedder driving the store
// directly — but it must behave as documented rather than deadlock or
// silently no-op.
func TestSaveWithoutLoadIsUnconditional(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	seeder := newTestStore(t, client, nil)
	if _, _, err := seeder.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	fresh := newTestStore(t, client, nil)
	if got := fresh.Version(); got != auth.KeyRingVersionUnknown {
		t.Fatalf("Version() = %d before any read, want unknown", got)
	}
	ring := auth.NewKeyRing()
	key, err := ring.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := fresh.Save(ctx, ring); err != nil {
		t.Fatalf("Save without a prior Load: %v", err)
	}
	if got := fake.versionCount(testSecretName); got != 2 {
		t.Fatalf("%d versions, want 2", got)
	}
	if got := fresh.Version(); got != 2 {
		t.Fatalf("Version() = %d after the write, want 2", got)
	}
	stored, err := fresh.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !hasKey(stored, key.ID) {
		t.Fatal("the unconditional write did not land")
	}
}

// A node that died mid-write and came back must not be locked out by its
// own abandoned lease — the owner check is what lets it proceed without
// waiting out the deadline.
func TestReacquiresItsOwnAbandonedLease(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, func(c *Config) {
		c.NodeID = "node-a"
		c.LockWait = 300 * time.Millisecond
	})
	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Its own lease, still well within the deadline, left behind by a
	// crash between taking the lease and releasing it.
	own := lease{owner: "node-a", expires: time.Now().Add(time.Minute)}
	fake.setAnnotation(testSecretName, lockAnnotation, own.encode())

	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	start := time.Now()
	if err := s.Save(ctx, ring); err != nil {
		t.Fatalf("Save against this node's own stale lease: %v", err)
	}
	if waited := time.Since(start); waited > 250*time.Millisecond {
		t.Fatalf("Save waited %s on its own lease; it should take it straight over", waited)
	}
	if got := fake.versionCount(testSecretName); got != 2 {
		t.Fatalf("%d versions, want 2", got)
	}
}

// TestConcurrentRotationsDoNotLoseKeys is the fleet-level invariant the
// lease and the revision check exist for: several nodes rotating at the
// same moment, each reloading and re-applying on conflict exactly as
// parsec.mutateKeys does. Every key must survive. Without the guard the
// last writer's snapshot wins and the others' keys are gone.
func TestConcurrentRotationsDoNotLoseKeys(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	seeder := newTestStore(t, client, nil)
	if _, _, err := seeder.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	const nodes = 4
	ids := make([]string, nodes)
	errs := make([]error, nodes)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := range nodes {
		s := newTestStore(t, client, func(c *Config) { c.LockWait = 10 * time.Second })
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// The retry loop parsec.mutateKeys runs on every store.
			for attempt := 0; attempt < 8; attempt++ {
				ring, err := s.Load(ctx)
				if err != nil {
					errs[i] = err
					return
				}
				key, err := ring.Generate()
				if err != nil {
					errs[i] = err
					return
				}
				err = s.Save(ctx, ring)
				if err == nil {
					ids[i] = key.ID
					return
				}
				if !errors.Is(err, auth.ErrKeyRingConflict) {
					errs[i] = err
					return
				}
			}
			errs[i] = errors.New("gave up after 8 attempts")
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("node %d: %v", i, err)
		}
	}
	final, err := seeder.Load(ctx)
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	for i, id := range ids {
		if !hasKey(final, id) {
			t.Fatalf("node %d's key %q was lost; a concurrent rotation overwrote it", i, id)
		}
	}
	if got := fake.versionCount(testSecretName); got != nodes+1 {
		t.Fatalf("%d versions, want %d (one bootstrap + one per node)", got, nodes+1)
	}
}

func TestIsContention(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		// Documented: https://cloud.google.com/secret-manager/docs/etags
		"failed precondition": {status.Error(codes.FailedPrecondition, "etag"), true},
		"aborted":             {status.Error(codes.Aborted, "etag"), true},
		"not found":           {status.Error(codes.NotFound, "gone"), false},
		"permission denied":   {status.Error(codes.PermissionDenied, "iam"), false},
		"unavailable":         {status.Error(codes.Unavailable, "transport"), false},
		"nil":                 {nil, false},
	}
	for name, tc := range cases {
		if got := isContention(tc.err); got != tc.want {
			t.Errorf("%s: isContention = %v, want %v", name, got, tc.want)
		}
	}
}

func TestNewRejectsBadConfigBeforeDialing(t *testing.T) {
	// New must validate before building a client: a missing ProjectID
	// should be an immediate, readable error rather than a credentials
	// lookup that fails somewhere else entirely.
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("New with an empty config returned no error")
	} else if !strings.Contains(err.Error(), "ProjectID is required") {
		t.Fatalf("New: %v, want the config validation error", err)
	}

	_, err := New(context.Background(), Config{
		ProjectID:       "p",
		CredentialsFile: filepath.Join(t.TempDir(), "nope.json"),
	})
	if err == nil {
		t.Fatal("New with a missing credentials file returned no error")
	}
	if !strings.Contains(err.Error(), "gcpsecretmanager:") {
		t.Fatalf("New: %v, want an error naming this package", err)
	}
}

func TestNewWithClientRejectsNilClient(t *testing.T) {
	if _, err := NewWithClient(Config{ProjectID: "p"}, nil); err == nil {
		t.Fatal("NewWithClient(nil) returned no error")
	}
}

// TestEnsureAdoptsTheWinnersRingAfterAConflict stages the bootstrap race
// deterministically. TestConcurrentBootstrapAdoptsOneRing asserts the
// outcome, but which goroutine reaches Save first is up to the scheduler,
// so the adopt path can go unexercised while the test still passes. Here
// the losing node is forced to believe the secret is empty when it is
// not, which is exactly what a node that read a moment too early sees.
func TestEnsureAdoptsTheWinnersRingAfterAConflict(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	winner := newTestStore(t, client, nil)
	winnerRing, _, err := winner.Ensure(ctx)
	if err != nil {
		t.Fatalf("winner Ensure: %v", err)
	}

	loser := newTestStore(t, client, nil)
	var blinded bool
	fake.setFaults(func(method string) error {
		if method == "AccessSecretVersion" && !blinded {
			blinded = true
			return status.Error(codes.NotFound, "no versions yet")
		}
		return nil
	})

	adopted, bootstrapped, err := loser.Ensure(ctx)
	if err != nil {
		t.Fatalf("loser Ensure: %v", err)
	}
	if !blinded {
		t.Fatal("the staged race never happened; the test proved nothing")
	}
	if bootstrapped {
		t.Fatal("the losing node reported a bootstrap; it minted a ring nobody else has")
	}
	if adopted.ActiveID() != winnerRing.ActiveID() {
		t.Fatalf("adopted active key %q, want the winner's %q", adopted.ActiveID(), winnerRing.ActiveID())
	}
	if got := fake.versionCount(testSecretName); got != 1 {
		t.Fatalf("%d versions after the losing bootstrap, want 1", got)
	}
}

// A poll that succeeds and a load that then fails is a different branch
// from a failing poll, and the same rule applies: report it and carry on.
func TestWatchSurvivesLoadFailures(t *testing.T) {
	fake, client := startFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := newTestStore(t, client, nil)
	writer := newTestStore(t, client, nil)
	if _, _, err := watcher.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	hookErrs := make(chan error, 16)
	watcher.SetErrorHook(func(err error) {
		select {
		case hookErrs <- err:
		default:
		}
	})

	// Read on the writer's side first: once the fault is installed the
	// only AccessSecretVersion calls left are the watcher's, which is
	// what this test is about. Save does not go through that RPC.
	remote, err := writer.Load(ctx)
	if err != nil {
		t.Fatalf("writer Load: %v", err)
	}

	var loads int
	fake.setFaults(func(method string) error {
		if method != "AccessSecretVersion" {
			return nil
		}
		loads++
		if loads <= 2 {
			return status.Error(codes.Internal, "backend hiccup")
		}
		return nil
	})

	changes := make(chan *auth.KeyRing, 4)
	done := make(chan error, 1)
	go func() { done <- watcher.Watch(ctx, func(r *auth.KeyRing) { changes <- r }) }()

	rotated, err := remote.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := remote.Promote(rotated.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := writer.Save(ctx, remote); err != nil {
		t.Fatalf("writer Save: %v", err)
	}

	select {
	case <-hookErrs:
	case err := <-done:
		t.Fatalf("Watch returned %v instead of reporting the failed load", err)
	case <-time.After(3 * time.Second):
		t.Fatal("a failed load was never reported to the error hook")
	}

	select {
	case got := <-changes:
		if got.ActiveID() != rotated.ID {
			t.Fatalf("watch delivered %q, want %q", got.ActiveID(), rotated.ID)
		}
	case err := <-done:
		t.Fatalf("Watch returned %v instead of recovering", err)
	case <-time.After(3 * time.Second):
		t.Fatal("watch never recovered from the failed loads")
	}
}

// Releasing the lease is best effort: it expires on its own, so a failure
// there costs at most one LockTTL of rotation latency. Failing the
// rotation over it would be strictly worse — the keys are already written.
func TestFailedLeaseReleaseDoesNotFailTheWrite(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	reported := make(chan error, 4)
	s.SetErrorHook(func(err error) {
		select {
		case reported <- err:
		default:
		}
	})

	// Everything works until the write lands; then the release fails.
	var written bool
	fake.setFaults(func(method string) error {
		if method == "AddSecretVersion" {
			written = true
			return nil
		}
		if written && method == "GetSecret" {
			return status.Error(codes.Unavailable, "gone at the worst moment")
		}
		return nil
	})

	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := s.Save(ctx, ring); err != nil {
		t.Fatalf("Save failed because the lease release did: %v", err)
	}
	if got := fake.versionCount(testSecretName); got != 2 {
		t.Fatalf("%d versions, want 2", got)
	}
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("the failed release was not reported; it would be invisible")
	}
}

// An abandoned lease must not outlive its deadline as a permanent block,
// and a release that lands after somebody else took the lease must not
// clear theirs.
func TestReleaseLeavesAStolenLeaseAlone(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Between the write and the release, another node takes the lease.
	stolen := lease{owner: "other-node", expires: time.Now().Add(time.Minute)}
	var written bool
	fake.setFaults(func(method string) error {
		if method == "AddSecretVersion" && !written {
			written = true
			if sec, ok := fake.secrets[testSecretName]; ok {
				if sec.pb.Annotations == nil {
					sec.pb.Annotations = map[string]string{}
				}
				sec.pb.Annotations[lockAnnotation] = stolen.encode()
				sec.etag++
			}
		}
		return nil
	})

	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := s.Save(ctx, ring); err != nil {
		t.Fatalf("Save: %v", err)
	}

	sec, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: testSecretName})
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got := sec.GetAnnotations()[lockAnnotation]; got != stolen.encode() {
		t.Fatalf("lease annotation = %q, want the other node's %q left untouched", got, stolen.encode())
	}
}

// An RPC failure on the write itself must surface, and must not move this
// node's idea of the revision — a store that believes it wrote would send
// its next compare-and-set against a revision that never existed.
func TestFailedWriteSurfacesAndLeavesTheRevisionAlone(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	before := s.Version()

	fake.setFaults(func(method string) error {
		if method == "AddSecretVersion" {
			return status.Error(codes.PermissionDenied, "missing secretmanager.versions.add")
		}
		return nil
	})
	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	err = s.Save(ctx, ring)
	if err == nil {
		t.Fatal("Save reported success after the write was refused")
	}
	if errors.Is(err, auth.ErrKeyRingConflict) {
		t.Fatalf("a permission failure was reported as a conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "add version") {
		t.Fatalf("error %v does not say which operation failed", err)
	}
	if got := s.Version(); got != before {
		t.Fatalf("Version() moved to %d after a failed write, want %d", got, before)
	}
	if got := fake.versionCount(testSecretName); got != 1 {
		t.Fatalf("%d versions, want 1", got)
	}
}

// A secret the caller cannot read is a configuration or IAM problem, and
// must be reported as such rather than mistaken for "not created yet" and
// quietly overwritten with a fresh ring.
func TestUnreadableSecretIsNotMistakenForAnEmptyOne(t *testing.T) {
	fake, client := startFake(t)
	ctx := context.Background()

	s := newTestStore(t, client, nil)
	if _, _, err := s.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	fresh := newTestStore(t, client, nil)
	fake.setFaults(func(method string) error {
		if method == "AccessSecretVersion" || method == "GetSecret" {
			return status.Error(codes.PermissionDenied, "caller lacks secretmanager.versions.access")
		}
		return nil
	})

	_, bootstrapped, err := fresh.Ensure(ctx)
	if err == nil {
		t.Fatalf("Ensure against an unreadable secret succeeded (bootstrapped=%v)", bootstrapped)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a permission failure was reported as an empty store: %v", err)
	}
	if got := fake.versionCount(testSecretName); got != 1 {
		t.Fatalf("%d versions; an unreadable ring must never be overwritten", got)
	}
}
