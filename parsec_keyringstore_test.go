package parsec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/frankbardon/parsec/auth"
)

// sharedStore is a stand-in for a keyring store living outside this
// module — the Google Secret Manager store in stores/gcpsecretmanager, or
// an embedder's own. It implements the optional halves of the contract
// (revision, error hook, bootstrap) so the wiring in parsec.New is
// exercised against interfaces rather than against the concrete Redis
// type it used to type-switch on.
type sharedStore struct {
	mu       sync.Mutex
	body     []byte
	version  int64
	loads    int
	saves    int
	failNext error

	watch   chan struct{}
	onError func(error)
}

func newSharedStore() *sharedStore {
	return &sharedStore{version: auth.KeyRingVersionUnknown, watch: make(chan struct{})}
}

func (s *sharedStore) Load(context.Context) (*auth.KeyRing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.body == nil {
		s.version = 0
		return nil, os.ErrNotExist
	}
	return auth.DecodeKeyRing(s.body)
}

func (s *sharedStore) Save(_ context.Context, r *auth.KeyRing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failNext; err != nil {
		s.failNext = nil
		return err
	}
	body, err := auth.EncodeKeyRing(r)
	if err != nil {
		return err
	}
	s.saves++
	s.body = body
	s.version++
	return nil
}

func (s *sharedStore) Watch(ctx context.Context, _ func(*auth.KeyRing)) error {
	<-ctx.Done()
	return nil
}

func (s *sharedStore) Version() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

func (s *sharedStore) SetErrorHook(f func(error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onError = f
}

func (s *sharedStore) reportError(err error) {
	s.mu.Lock()
	f := s.onError
	s.mu.Unlock()
	if f != nil {
		f(err)
	}
}

func (s *sharedStore) stats() (loads, saves int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads, s.saves
}

// storedRing decodes what the store currently holds.
func (s *sharedStore) storedRing(t *testing.T) *auth.KeyRing {
	t.Helper()
	s.mu.Lock()
	body := s.body
	s.mu.Unlock()
	if body == nil {
		t.Fatal("store holds no ring")
	}
	ring, err := auth.DecodeKeyRing(body)
	if err != nil {
		t.Fatalf("decode stored ring: %v", err)
	}
	return ring
}

func TestCustomKeyRingStoreBootstrapsAndPersistsRotations(t *testing.T) {
	store := newSharedStore()
	p, err := New(Options{KeyRingStore: store, Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.KeyringStore() != auth.KeyRingStore(store) {
		t.Fatalf("KeyringStore = %T, want the configured store", p.KeyringStore())
	}
	if _, saves := store.stats(); saves != 1 {
		t.Fatalf("boot wrote %d rings, want 1 (bootstrap)", saves)
	}
	if p.KeyringPath() != "" {
		t.Errorf("KeyringPath = %q, want empty for a store-backed ring", p.KeyringPath())
	}

	key, err := p.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if _, err := store.storedRing(t).Get(key.ID); err != nil {
		t.Fatalf("rotation was not persisted to the store: %v", err)
	}

	// A second node against the same store must adopt the ring, not mint
	// its own — otherwise tokens minted by one are rejected by the other.
	second, err := New(Options{KeyRingStore: store, Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New (second node): %v", err)
	}
	if got, want := second.KeyRing().ActiveID(), p.KeyRing().ActiveID(); got != want {
		t.Fatalf("second node signs with %q, first with %q", got, want)
	}
}

func TestCustomKeyRingStoreOutranksStateDir(t *testing.T) {
	store := newSharedStore()
	dir := t.TempDir()
	var buf bytes.Buffer

	p, err := New(Options{KeyRingStore: store, StateDir: dir, Logger: bufLogger(&buf)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.KeyringStore() != auth.KeyRingStore(store) {
		t.Fatalf("KeyringStore = %T, want the configured store", p.KeyringStore())
	}
	if _, err := os.Stat(filepath.Join(dir, auth.KeyringFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("keyring.json exists alongside a configured store (stat err %v); the keys would be in two places", err)
	}
	// The warning is the point: a StateDir in the config reads like a
	// durable backup of the signing keys, and it is not one.
	if !strings.Contains(buf.String(), "keyring.json is not read or written") {
		t.Fatalf("boot log does not warn that StateDir is ignored:\n%s", buf.String())
	}
}

func TestCustomKeyRingStoreOutranksRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	store := newSharedStore()
	var buf bytes.Buffer

	p, err := New(Options{KeyRingStore: store, RedisAddr: mr.Addr(), Logger: bufLogger(&buf)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.KeyringStore() != auth.KeyRingStore(store) {
		t.Fatalf("KeyringStore = %T, want the configured store", p.KeyringStore())
	}
	if mr.Exists("parsec:keyring") {
		t.Error("parsec:keyring exists in redis; the keys would be in two places")
	}
	// "We run Redis" is otherwise a reasonable reason to believe the keys
	// are in it — and to back up the wrong thing.
	if !strings.Contains(buf.String(), "holds no copy of the signing keys") {
		t.Fatalf("boot log does not warn that redis is not holding the keyring:\n%s", buf.String())
	}
	if p.Persistence() != "redis" {
		t.Errorf("Persistence = %q, want redis: the keyring moved, nothing else did", p.Persistence())
	}
}

func TestCustomKeyRingStoreReportsVersionAndExternalBackend(t *testing.T) {
	store := newSharedStore()
	p, err := New(Options{KeyRingStore: store, Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := gatherKeyringMetrics(t, p)
	if v, ok := got[`parsec_keyring_backend{backend="external"}`]; !ok || v != 1 {
		t.Errorf("backend gauge for external = %v (present=%v), want 1", v, ok)
	}
	if v, ok := got[`parsec_keyring_backend{backend="redis"}`]; !ok || v != 0 {
		t.Errorf("backend gauge for redis = %v (present=%v), want 0", v, ok)
	}
	// The revision comes from auth.KeyRingVersioner. Reporting -1 here
	// would hide divergence between nodes sharing the store.
	if v, ok := got["parsec_keyring_version"]; !ok || v != 1 {
		t.Errorf("version gauge = %v (present=%v), want 1", v, ok)
	}
	if _, err := p.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if v := gatherKeyringMetrics(t, p)["parsec_keyring_version"]; v != 2 {
		t.Errorf("version gauge after rotation = %v, want 2", v)
	}
}

// A conflict is the shared-store contract's whole point: the losing
// writer must reload and re-apply rather than overwrite. mutateKeys does
// that for any store returning auth.ErrKeyRingConflict, not just Redis.
func TestCustomKeyRingStoreConflictReloadsAndReapplies(t *testing.T) {
	store := newSharedStore()
	p, err := New(Options{KeyRingStore: store, Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Another node rotates behind this one's back, then this node's next
	// write is rejected as stale.
	remote := store.storedRing(t)
	remoteKey, err := remote.Generate()
	if err != nil {
		t.Fatalf("remote generate: %v", err)
	}
	if err := store.Save(context.Background(), remote); err != nil {
		t.Fatalf("remote save: %v", err)
	}
	store.mu.Lock()
	store.failNext = auth.ErrKeyRingConflict
	store.mu.Unlock()

	local, err := p.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey after a conflict: %v", err)
	}

	stored := store.storedRing(t)
	if _, err := stored.Get(remoteKey.ID); err != nil {
		t.Fatalf("the other node's rotation was erased: %v", err)
	}
	if _, err := stored.Get(local.ID); err != nil {
		t.Fatalf("this node's key was not re-applied after the conflict: %v", err)
	}
}

func TestCustomKeyRingStoreErrorHookFeedsTheCounter(t *testing.T) {
	store := newSharedStore()
	p, err := New(Options{KeyRingStore: store, Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if before := gatherKeyringMetrics(t, p)["parsec_keyring_reconcile_errors_total"]; before != 0 {
		t.Fatalf("reconcile errors start at %v, want 0", before)
	}

	// parsec installs the hook through auth.KeyRingErrorReporter; a store
	// whose watch errors vanish leaves a stale ring invisible.
	store.reportError(errors.New("watch dropped"))

	if got := gatherKeyringMetrics(t, p)["parsec_keyring_reconcile_errors_total"]; got != 1 {
		t.Fatalf("reconcile errors = %v, want 1", got)
	}
}

func TestReloadKeysNamesEveryStoreOption(t *testing.T) {
	p, err := New(Options{Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = p.ReloadKeys()
	if err == nil {
		t.Fatal("ReloadKeys on an ephemeral ring returned nil")
	}
	if !strings.Contains(err.Error(), "KeyRingStore") {
		t.Fatalf("error %q does not mention the KeyRingStore option", err)
	}
}
