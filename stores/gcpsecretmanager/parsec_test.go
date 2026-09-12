package gcpsecretmanager

import (
	"context"
	"testing"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
)

// These tests drive parsec itself against the store, which is only
// possible from this module — the root module must not import an adapter,
// so its own coverage of Options.KeyRingStore uses a stub. What that stub
// cannot prove is that the real store satisfies what parsec.New, the
// rotation path and the keyring watcher actually ask of it.

func newParsecStore(t *testing.T, client *secretmanager.Client, tweak func(*Config)) *KeyRingStore {
	t.Helper()
	cfg := Config{
		ProjectID:         "test-project",
		ReconcileInterval: 20 * time.Millisecond,
		LockWait:          5 * time.Second,
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

func TestParsecBootstrapsAndRotatesThroughTheStore(t *testing.T) {
	_, client := startFake(t)
	store := newParsecStore(t, client, nil)

	p, err := parsec.New(parsec.Options{KeyRingStore: store})
	if err != nil {
		t.Fatalf("parsec.New: %v", err)
	}
	if p.KeyringStore() != auth.KeyRingStore(store) {
		t.Fatalf("KeyringStore = %T, want the Secret Manager store", p.KeyringStore())
	}
	if got := store.Version(); got != 1 {
		t.Fatalf("store version after boot = %d, want 1 (the bootstrap write)", got)
	}

	key, err := p.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := p.PromoteKey(key.ID); err != nil {
		t.Fatalf("PromoteKey: %v", err)
	}

	// Read it back through a second store: what matters is that the
	// rotation reached Secret Manager, not that this process remembers it.
	verify := newParsecStore(t, client, nil)
	stored, err := verify.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.ActiveID() != key.ID {
		t.Fatalf("stored active key = %q, want the promoted %q", stored.ActiveID(), key.ID)
	}
	if got := verify.Version(); got != 3 {
		t.Fatalf("stored version = %d, want 3 (bootstrap + generate + promote)", got)
	}
}

// The user-visible failure when a shared store is wired wrong: a token
// minted on one node is rejected by the next.
func TestTokensMintedOnOneNodeVerifyOnAnother(t *testing.T) {
	_, client := startFake(t)

	first, err := parsec.New(parsec.Options{KeyRingStore: newParsecStore(t, client, nil)})
	if err != nil {
		t.Fatalf("parsec.New (first node): %v", err)
	}
	second, err := parsec.New(parsec.Options{KeyRingStore: newParsecStore(t, client, nil)})
	if err != nil {
		t.Fatalf("parsec.New (second node): %v", err)
	}

	token, _, err := first.Issuer().IssueMgmt("operator", time.Hour)
	if err != nil {
		t.Fatalf("IssueMgmt: %v", err)
	}
	if _, err := second.Verifier().Verify(token, auth.TypeMgmt); err != nil {
		t.Fatalf("token minted on the first node was rejected by the second: %v", err)
	}
}

// A rotation on one node must reach the other through the watcher parsec
// starts in Run — no SIGHUP, no restart.
func TestRunPicksUpARotationFromAnotherNode(t *testing.T) {
	_, client := startFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher, err := parsec.New(parsec.Options{
		KeyRingStore:        newParsecStore(t, client, nil),
		KeyringPollInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("parsec.New: %v", err)
	}
	go func() { _ = watcher.Run(ctx) }()

	rotator, err := parsec.New(parsec.Options{KeyRingStore: newParsecStore(t, client, nil)})
	if err != nil {
		t.Fatalf("parsec.New (rotator): %v", err)
	}
	key, err := rotator.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := rotator.PromoteKey(key.ID); err != nil {
		t.Fatalf("PromoteKey: %v", err)
	}

	deadline := time.After(10 * time.Second)
	for {
		if watcher.KeyRing().ActiveID() == key.ID {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the watching node still signs with %q; the rotation to %q never arrived",
				watcher.KeyRing().ActiveID(), key.ID)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// And the token the rotated ring mints must verify on the node that
	// picked the rotation up.
	token, _, err := rotator.Issuer().IssueMgmt("operator", time.Hour)
	if err != nil {
		t.Fatalf("IssueMgmt: %v", err)
	}
	if _, err := watcher.Verifier().Verify(token, auth.TypeMgmt); err != nil {
		t.Fatalf("token under the new key rejected after reload: %v", err)
	}
}

// parsec.mutateKeys reloads and re-applies on auth.ErrKeyRingConflict.
// This is that loop running against the real store rather than a stub, so
// a conflict the store raises for its own reasons still resolves.
func TestRotationSurvivesAConflictFromTheStore(t *testing.T) {
	fake, client := startFake(t)

	a, err := parsec.New(parsec.Options{KeyRingStore: newParsecStore(t, client, nil)})
	if err != nil {
		t.Fatalf("parsec.New (a): %v", err)
	}
	b, err := parsec.New(parsec.Options{KeyRingStore: newParsecStore(t, client, nil)})
	if err != nil {
		t.Fatalf("parsec.New (b): %v", err)
	}

	// b rotates; a is now holding a stale revision and does not know it.
	fromB, err := b.GenerateKey()
	if err != nil {
		t.Fatalf("b GenerateKey: %v", err)
	}
	fromA, err := a.GenerateKey()
	if err != nil {
		t.Fatalf("a GenerateKey after b rotated: %v", err)
	}

	final, err := newParsecStore(t, client, nil).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := final.Get(fromB.ID); err != nil {
		t.Fatalf("b's key was erased by a's rotation: %v", err)
	}
	if _, err := final.Get(fromA.ID); err != nil {
		t.Fatalf("a's key was not re-applied after the conflict: %v", err)
	}
	// Three writes, not four: the rejected attempt must not have reached
	// Secret Manager at all, or the conflict check came too late to stop
	// it and only the retry saved us.
	if got := fake.callCount("AddSecretVersion"); got != 3 {
		t.Fatalf("%d versions written, want 3 (bootstrap, b's rotation, a's retry)", got)
	}
	if got := fake.versionCount(testSecretName); got != 3 {
		t.Fatalf("%d versions stored, want 3", got)
	}
}
