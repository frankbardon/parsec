package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestEncodeKeyRingStampsFormatVersion is the reason the codec is
// exported at all. A store outside this module that marshals Snapshot
// itself writes format_version "", which every loader then treats as a
// legacy v1 ring — silently dropping the per-key Alg that v2 added.
func TestEncodeKeyRingStampsFormatVersion(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	body, err := EncodeKeyRing(r)
	if err != nil {
		t.Fatalf("EncodeKeyRing: %v", err)
	}
	var probe struct {
		FormatVersion string `json:"format_version"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe.FormatVersion != KeyRingFormatVersion {
		t.Fatalf("format_version = %q, want %q", probe.FormatVersion, KeyRingFormatVersion)
	}
}

func TestEncodeDecodeKeyRingRoundTrip(t *testing.T) {
	r := NewKeyRing()
	hs, err := r.Generate()
	if err != nil {
		t.Fatalf("generate hs256: %v", err)
	}
	ed, err := r.GenerateEd25519()
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	if err := r.Promote(ed.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}

	body, err := EncodeKeyRing(r)
	if err != nil {
		t.Fatalf("EncodeKeyRing: %v", err)
	}
	back, err := DecodeKeyRing(body)
	if err != nil {
		t.Fatalf("DecodeKeyRing: %v", err)
	}
	if back.ActiveID() != ed.ID {
		t.Fatalf("active key = %q, want %q", back.ActiveID(), ed.ID)
	}
	got, err := back.Get(hs.ID)
	if err != nil {
		t.Fatalf("hs256 key missing after round trip: %v", err)
	}
	if got.Alg != AlgHS256 {
		t.Fatalf("hs256 key came back as %q", got.Alg)
	}
	if signer, err := back.Get(ed.ID); err != nil || signer.Alg != AlgEdDSA {
		t.Fatalf("ed25519 key came back as %+v (err %v)", signer, err)
	}
}

func TestEncodeKeyRingRejectsNil(t *testing.T) {
	if _, err := EncodeKeyRing(nil); err == nil {
		t.Fatal("EncodeKeyRing(nil) returned no error")
	}
}

func TestDecodeKeyRingFormatVersions(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	body, err := EncodeKeyRing(r)
	if err != nil {
		t.Fatalf("EncodeKeyRing: %v", err)
	}

	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, v := range []string{"", "1", "2"} {
		snap.FormatVersion = v
		raw, err := json.Marshal(snap)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := DecodeKeyRing(raw); err != nil {
			t.Fatalf("DecodeKeyRing with format_version %q: %v", v, err)
		}
	}

	snap.FormatVersion = "99"
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = DecodeKeyRing(raw)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("DecodeKeyRing with a future format version: %v, want a refusal", err)
	}

	if _, err := DecodeKeyRing([]byte("{not json")); err == nil {
		t.Fatal("DecodeKeyRing accepted malformed JSON")
	}
}

// memStore is the minimum KeyRingStore — no bootstrap hook, no revision.
// EnsureKeyRingStore has to cope with it, because an embedder's store is
// allowed to implement nothing beyond the three required methods.
type memStore struct {
	body      []byte
	saves     int
	saveErr   error
	loadErr   error
	onSave    func()
	saveOnce  bool
	loadCount int
}

func (m *memStore) Load(context.Context) (*KeyRing, error) {
	m.loadCount++
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.body == nil {
		return nil, os.ErrNotExist
	}
	return DecodeKeyRing(m.body)
}

func (m *memStore) Save(_ context.Context, r *KeyRing) error {
	if m.onSave != nil && !m.saveOnce {
		m.saveOnce = true
		m.onSave()
	}
	if m.saveErr != nil {
		err := m.saveErr
		m.saveErr = nil
		return err
	}
	body, err := EncodeKeyRing(r)
	if err != nil {
		return err
	}
	m.saves++
	m.body = body
	return nil
}

func (m *memStore) Watch(ctx context.Context, _ func(*KeyRing)) error {
	<-ctx.Done()
	return nil
}

func TestEnsureKeyRingStoreBootstrapsEmptyStore(t *testing.T) {
	m := &memStore{}
	ring, bootstrapped, err := EnsureKeyRingStore(context.Background(), m)
	if err != nil {
		t.Fatalf("EnsureKeyRingStore: %v", err)
	}
	if !bootstrapped {
		t.Fatal("bootstrap not reported for an empty store")
	}
	if ring.ActiveID() == "" {
		t.Fatal("bootstrapped ring has no active key")
	}
	if m.saves != 1 {
		t.Fatalf("store saw %d saves, want 1", m.saves)
	}

	again, bootstrapped, err := EnsureKeyRingStore(context.Background(), m)
	if err != nil {
		t.Fatalf("second EnsureKeyRingStore: %v", err)
	}
	if bootstrapped {
		t.Fatal("second call bootstrapped instead of loading")
	}
	if again.ActiveID() != ring.ActiveID() {
		t.Fatalf("reloaded active key %q, want %q", again.ActiveID(), ring.ActiveID())
	}
}

// A store that loses the bootstrap race must adopt the winner's ring. The
// failure this guards is two nodes each minting a ring, after which every
// token one signs is rejected by the other.
func TestEnsureKeyRingStoreAdoptsWinnerOnConflict(t *testing.T) {
	winner := NewKeyRing()
	if _, err := winner.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	body, err := EncodeKeyRing(winner)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	m := &memStore{saveErr: ErrKeyRingConflict}
	// The other node's write lands between this node's Load and Save.
	m.onSave = func() { m.body = body }

	ring, bootstrapped, err := EnsureKeyRingStore(context.Background(), m)
	if err != nil {
		t.Fatalf("EnsureKeyRingStore: %v", err)
	}
	if bootstrapped {
		t.Fatal("the losing node reported a bootstrap")
	}
	if ring.ActiveID() != winner.ActiveID() {
		t.Fatalf("adopted active key %q, want the winner's %q", ring.ActiveID(), winner.ActiveID())
	}
}

func TestEnsureKeyRingStorePropagatesLoadFailures(t *testing.T) {
	sentinel := errors.New("backend is on fire")
	m := &memStore{loadErr: sentinel}
	if _, _, err := EnsureKeyRingStore(context.Background(), m); !errors.Is(err, sentinel) {
		t.Fatalf("EnsureKeyRingStore: %v, want %v", err, sentinel)
	}
	if m.saves != 0 {
		t.Fatalf("a failed Load still led to %d saves; an unreadable store must not be overwritten", m.saves)
	}
}

// bootstrapStore implements the optional KeyRingBootstrapper, which a
// shared store uses to make the initial write conditional in its own
// terms rather than through the generic read-then-write.
type bootstrapStore struct {
	memStore
	called int
	ring   *KeyRing
}

func (b *bootstrapStore) Ensure(context.Context) (*KeyRing, bool, error) {
	b.called++
	return b.ring, true, nil
}

func TestEnsureKeyRingStorePrefersTheStoresOwnBootstrap(t *testing.T) {
	ring := NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	b := &bootstrapStore{ring: ring}

	got, bootstrapped, err := EnsureKeyRingStore(context.Background(), b)
	if err != nil {
		t.Fatalf("EnsureKeyRingStore: %v", err)
	}
	if b.called != 1 {
		t.Fatalf("store's own Ensure called %d times, want 1", b.called)
	}
	if b.loadCount != 0 || b.saves != 0 {
		t.Fatalf("generic path ran anyway: %d loads, %d saves", b.loadCount, b.saves)
	}
	if !bootstrapped || got.ActiveID() != ring.ActiveID() {
		t.Fatalf("Ensure returned (%q, %v), want the store's own answer", got.ActiveID(), bootstrapped)
	}
}

func TestRedisStoreSatisfiesTheOptionalInterfaces(t *testing.T) {
	// The in-tree shared store must keep satisfying the interfaces
	// parsec.New now wires through, or the version gauge and the
	// reconcile-error counter go quiet without anything failing.
	var s any = NewRedisKeyRingStore(nil)
	if _, ok := s.(KeyRingVersioner); !ok {
		t.Error("RedisKeyRingStore no longer implements KeyRingVersioner")
	}
	if _, ok := s.(KeyRingErrorReporter); !ok {
		t.Error("RedisKeyRingStore no longer implements KeyRingErrorReporter")
	}
	if _, ok := s.(KeyRingBootstrapper); !ok {
		t.Error("RedisKeyRingStore no longer implements KeyRingBootstrapper")
	}
}
