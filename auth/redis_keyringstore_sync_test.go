package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// miniredisStore builds a store against an in-process Redis. Unlike the
// tests in redis_keyringstore_test.go this needs no PARSEC_REDIS_ADDR, so
// it runs in CI — which matters, because the behavior under test here is
// exactly what nothing was covering.
func miniredisStore(t *testing.T) (*RedisKeyRingStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisKeyRingStore(client), mr
}

func ringWithKeys(t *testing.T, n int) *KeyRing {
	t.Helper()
	r := NewKeyRing()
	for i := 0; i < n; i++ {
		if _, err := r.Generate(); err != nil {
			t.Fatalf("generate: %v", err)
		}
	}
	return r
}

// A save derived from a stale snapshot must be refused. Previously Save
// marshalled the caller's ring once and its WATCH only covered the
// transaction window, so a node holding an older ring would bump the
// version and overwrite whatever rotation landed in between — a silent
// lost update of signing keys.
func TestRedisKeyRingStore_StaleSaveConflicts(t *testing.T) {
	nodeA, mr := miniredisStore(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	nodeB := NewRedisKeyRingStore(client)
	ctx := context.Background()

	// Both nodes load the same starting point.
	seed := ringWithKeys(t, 1)
	if err := nodeA.Save(ctx, seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	ringA, err := nodeA.Load(ctx)
	if err != nil {
		t.Fatalf("node A load: %v", err)
	}
	ringB, err := nodeB.Load(ctx)
	if err != nil {
		t.Fatalf("node B load: %v", err)
	}

	// Node A rotates and wins.
	keyA, err := ringA.Generate()
	if err != nil {
		t.Fatalf("node A generate: %v", err)
	}
	if err := nodeA.Save(ctx, ringA); err != nil {
		t.Fatalf("node A save: %v", err)
	}

	// Node B rotates from its now-stale ring and must be refused.
	if _, err := ringB.Generate(); err != nil {
		t.Fatalf("node B generate: %v", err)
	}
	err = nodeB.Save(ctx, ringB)
	if !errors.Is(err, ErrKeyRingConflict) {
		t.Fatalf("node B save = %v, want ErrKeyRingConflict", err)
	}

	// Node A's key must still be there.
	stored, err := nodeA.Load(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := stored.Get(keyA.ID); err != nil {
		t.Errorf("node A's key %q was lost: %v", keyA.ID, err)
	}
}

// The documented recovery: reload, re-apply, save.
func TestRedisKeyRingStore_ConflictResolvesAfterReload(t *testing.T) {
	store, mr := miniredisStore(t)
	other := NewRedisKeyRingStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	ctx := context.Background()

	if err := store.Save(ctx, ringWithKeys(t, 1)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mine, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Someone else rotates.
	theirs, err := other.Load(ctx)
	if err != nil {
		t.Fatalf("other load: %v", err)
	}
	theirKey, err := theirs.Generate()
	if err != nil {
		t.Fatalf("other generate: %v", err)
	}
	if err := other.Save(ctx, theirs); err != nil {
		t.Fatalf("other save: %v", err)
	}

	if _, err := mine.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := store.Save(ctx, mine); !errors.Is(err, ErrKeyRingConflict) {
		t.Fatalf("save = %v, want conflict", err)
	}

	// Reload, re-apply, save — now it lands and keeps both keys.
	fresh, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	myKey, err := fresh.Generate()
	if err != nil {
		t.Fatalf("re-generate: %v", err)
	}
	if err := store.Save(ctx, fresh); err != nil {
		t.Fatalf("save after reload: %v", err)
	}
	final, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	for _, id := range []string{theirKey.ID, myKey.ID} {
		if _, err := final.Get(id); err != nil {
			t.Errorf("key %q missing from final ring: %v", id, err)
		}
	}
}

// A store that never loaded has no baseline to compare, so its first save
// is unconditional rather than an error.
func TestRedisKeyRingStore_SaveWithoutLoadIsUnconditional(t *testing.T) {
	store, _ := miniredisStore(t)
	ctx := context.Background()
	if got := store.Version(); got != versionUnknown {
		t.Fatalf("Version = %d, want %d before any read", got, versionUnknown)
	}
	if err := store.Save(ctx, ringWithKeys(t, 1)); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if got := store.Version(); got != 1 {
		t.Errorf("Version = %d, want 1 after save", got)
	}
}

// Two nodes booting against a fresh Redis must converge on one ring. The
// loser adopts the winner's keys instead of overwriting them — otherwise
// it would sign with keys no other node can verify.
func TestRedisKeyRingStore_ConcurrentBootstrapConverges(t *testing.T) {
	_, mr := miniredisStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	actives := make([]string, 2)
	errs := make([]error, 2)
	for i := range actives {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer c.Close()
			ring, _, err := NewRedisKeyRingStore(c).Ensure(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			actives[i] = ring.ActiveID()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("node %d Ensure: %v", i, err)
		}
	}
	if actives[0] != actives[1] {
		t.Errorf("nodes bootstrapped different rings: %q vs %q", actives[0], actives[1])
	}
}

// Load must never pair a snapshot with the wrong version, or the CAS in
// Save would compare against a baseline that does not describe the body
// the caller holds.
func TestRedisKeyRingStore_LoadPairsBodyWithVersion(t *testing.T) {
	store, _ := miniredisStore(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		r := ringWithKeys(t, i)
		if err := store.Save(ctx, r); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		if _, err := store.Load(ctx); err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
		if got := store.Version(); got != int64(i) {
			t.Errorf("after save/load %d: Version = %d, want %d", i, got, i)
		}
	}
}

// Watch must converge even when the rotation event is never delivered.
// Redis pub/sub is at-most-once, so a node disconnected at publish time
// used to serve a stale ring until it restarted — the reconcile read is
// what bounds that.
func TestRedisKeyRingStore_WatchReconcilesWithoutEvent(t *testing.T) {
	store, mr := miniredisStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := store.Save(ctx, ringWithKeys(t, 1)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	watcher := NewRedisKeyRingStore(redis.NewClient(&redis.Options{Addr: mr.Addr()})).
		WithReconcileInterval(50 * time.Millisecond)
	if _, err := watcher.Load(ctx); err != nil {
		t.Fatalf("watcher load: %v", err)
	}

	seen := make(chan string, 8)
	go func() { _ = watcher.Watch(ctx, func(r *KeyRing) { seen <- r.ActiveID() }) }()

	// Establish that the subscription is live before testing the reconcile,
	// otherwise the read Watch performs on connect could be what notices
	// the change and the reconcile would go untested. A real Save publishes
	// an event, so receiving it proves the subscription is up.
	viaEvent := ringWithKeys(t, 1)
	eventKey, err := viaEvent.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := viaEvent.Promote(eventKey.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	writer := NewRedisKeyRingStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	if err := writer.Save(ctx, viaEvent); err != nil {
		t.Fatalf("writer save: %v", err)
	}
	if err := awaitActive(ctx, seen, eventKey.ID); err != nil {
		t.Fatalf("subscription never delivered the published rotation: %v", err)
	}

	// Now write a rotated ring into the keyspace directly, without going
	// through Save, so nothing is published on the events channel. This is
	// what a node sees when it was disconnected at publish time: the data
	// moved and the notification is gone for good. With the subscription
	// already live and idle, only the reconcile read can notice.
	fresh := ringWithKeys(t, 1)
	rotated, err := fresh.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := fresh.Promote(rotated.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	snap := fresh.Snapshot()
	snap.FormatVersion = keyringFormatVersion
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mr.Set("parsec:keyring", string(body))
	mr.Set("parsec:keyring:version", "99")

	deadline, cancelDeadline := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDeadline()
	if err := awaitActive(deadline, seen, rotated.ID); err != nil {
		t.Fatalf("watch never reconciled to the unannounced rotation: %v", err)
	}
}

// awaitActive waits for an onChange carrying the given active key id.
func awaitActive(ctx context.Context, seen <-chan string, want string) error {
	for {
		select {
		case got := <-seen:
			if got == want {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// onChange must not fire for a version this watcher already has, or every
// local rotation would trigger a pointless self-reload.
func TestRedisKeyRingStore_WatchDoesNotRefireSameVersion(t *testing.T) {
	store, mr := miniredisStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := store.Save(ctx, ringWithKeys(t, 1)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	watcher := NewRedisKeyRingStore(redis.NewClient(&redis.Options{Addr: mr.Addr()})).
		WithReconcileInterval(20 * time.Millisecond)
	if _, err := watcher.Load(ctx); err != nil {
		t.Fatalf("watcher load: %v", err)
	}

	var mu sync.Mutex
	var calls int
	go func() {
		_ = watcher.Watch(ctx, func(*KeyRing) {
			mu.Lock()
			calls++
			mu.Unlock()
		})
	}()

	// Several reconcile ticks with nothing changing.
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 0 {
		t.Errorf("onChange fired %d times with no version change, want 0", got)
	}
}

// Watch is a supervisor: it must keep running across a transport failure
// rather than returning and leaving the node frozen on old keys.
func TestRedisKeyRingStore_WatchSurvivesRedisRestart(t *testing.T) {
	store, mr := miniredisStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := store.Save(ctx, ringWithKeys(t, 1)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	watcher := NewRedisKeyRingStore(redis.NewClient(&redis.Options{Addr: mr.Addr()})).
		WithReconcileInterval(50 * time.Millisecond)
	if _, err := watcher.Load(ctx); err != nil {
		t.Fatalf("watcher load: %v", err)
	}

	seen := make(chan string, 4)
	done := make(chan error, 1)
	go func() { done <- watcher.Watch(ctx, func(r *KeyRing) { seen <- r.ActiveID() }) }()

	// Bounce Redis on the same port, the way a failover or restart does.
	// Every connection including the subscription dies; values survive.
	time.Sleep(100 * time.Millisecond)
	mr.Close()
	time.Sleep(100 * time.Millisecond)
	if err := mr.Restart(); err != nil {
		t.Fatalf("restart miniredis: %v", err)
	}

	select {
	case err := <-done:
		t.Fatalf("Watch returned after a redis bounce (err=%v); it must supervise itself", err)
	case <-time.After(300 * time.Millisecond):
	}

	// A rotation after the blip must still reach the watcher.
	fresh := ringWithKeys(t, 1)
	rotated, err := fresh.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := fresh.Promote(rotated.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	writer := NewRedisKeyRingStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	if err := writer.Save(ctx, fresh); err != nil {
		t.Fatalf("writer save: %v", err)
	}

	for {
		select {
		case got := <-seen:
			if got == rotated.ID {
				return
			}
		case <-ctx.Done():
			t.Fatal("watcher never saw the rotation published after the connection drop")
		}
	}
}

// Errors encountered while watching are reported, not swallowed, so a
// caller can count them.
func TestRedisKeyRingStore_ErrorHookFires(t *testing.T) {
	store, mr := miniredisStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := store.Save(ctx, ringWithKeys(t, 1)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := make(chan error, 8)
	watcher := NewRedisKeyRingStore(redis.NewClient(&redis.Options{Addr: mr.Addr()})).
		WithReconcileInterval(20 * time.Millisecond).
		WithErrorHook(func(err error) {
			select {
			case got <- err:
			default:
			}
		})
	go func() { _ = watcher.Watch(ctx, func(*KeyRing) {}) }()

	time.Sleep(50 * time.Millisecond)
	mr.Close() // every subsequent read and the subscription fail

	select {
	case err := <-got:
		if err == nil {
			t.Fatal("hook fired with a nil error")
		}
	case <-ctx.Done():
		t.Fatal("error hook never fired after redis went away")
	}
}

// An absent ring is a normal bootstrap condition, not a watch error.
func TestRedisKeyRingStore_LoadMissingIsNotExist(t *testing.T) {
	store, _ := miniredisStore(t)
	_, err := store.Load(context.Background())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load on empty redis = %v, want os.ErrNotExist", err)
	}
}
