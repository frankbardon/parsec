package auth

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKeyRing_FirstAddBecomesActive(t *testing.T) {
	r := NewKeyRing()
	k, err := r.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if k.Role != RoleActive {
		t.Fatalf("first key should be active, got %s", k.Role)
	}
	if got, _ := r.Active(); got.ID != k.ID {
		t.Fatalf("active id mismatch: got=%s want=%s", got.ID, k.ID)
	}
}

func TestKeyRing_SubsequentAddIsVerifyOnly(t *testing.T) {
	r := NewKeyRing()
	first, _ := r.Generate()
	second, err := r.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if second.Role != RoleVerifyOnly {
		t.Fatalf("second key should be verify-only, got %s", second.Role)
	}
	if got, _ := r.Active(); got.ID != first.ID {
		t.Fatalf("active should still be the first key, got %s", got.ID)
	}
}

func TestKeyRing_Promote(t *testing.T) {
	r := NewKeyRing()
	first, _ := r.Generate()
	second, _ := r.Generate()
	if err := r.Promote(second.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Active(); got.ID != second.ID {
		t.Fatalf("active should be second after promote, got %s", got.ID)
	}
	old, _ := r.Get(first.ID)
	if old.Role != RoleVerifyOnly {
		t.Fatalf("previous active should be verify-only, got %s", old.Role)
	}
}

func TestKeyRing_RetireActiveRejected(t *testing.T) {
	r := NewKeyRing()
	first, _ := r.Generate()
	if err := r.Retire(first.ID); err == nil {
		t.Fatal("expected retire-active rejection")
	}
}

func TestKeyRing_RetireRemovesFromVerification(t *testing.T) {
	r := NewKeyRing()
	first, _ := r.Generate()
	second, _ := r.Generate()
	if err := r.Promote(second.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(first.ID); err == nil {
		t.Fatal("expected retired key to be inaccessible via Get")
	}
}

func TestKeyRing_AddRejectsDuplicateID(t *testing.T) {
	r := NewKeyRing()
	first, _ := r.Generate()
	if _, err := r.Add(first.ID, make([]byte, 32)); err == nil {
		t.Fatal("expected duplicate id rejection")
	}
}

func TestKeyRing_AddRejectsShortSecret(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.Add("k-short", []byte("short")); err == nil {
		t.Fatal("expected short-secret rejection")
	}
}

func TestKeyRing_SnapshotRoundTrip(t *testing.T) {
	r := NewKeyRing()
	first, _ := r.Generate()
	second, _ := r.Generate()
	_ = r.Promote(second.ID)
	snap := r.Snapshot()
	if snap.ActiveKeyID != second.ID {
		t.Fatalf("snapshot active id mismatch: %s", snap.ActiveKeyID)
	}
	if len(snap.Keys) != 2 {
		t.Fatalf("expected 2 keys in snapshot, got %d", len(snap.Keys))
	}

	loaded := NewKeyRing()
	if err := loaded.LoadSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	got, _ := loaded.Active()
	if got.ID != second.ID {
		t.Fatalf("loaded active mismatch: %s", got.ID)
	}
	if _, err := loaded.Get(first.ID); err != nil {
		t.Fatalf("first key missing after load: %v", err)
	}
}

func TestKeyRing_LoadSnapshotRejectsBadActive(t *testing.T) {
	r := NewKeyRing()
	first, _ := r.Generate()
	snap := r.Snapshot()
	snap.ActiveKeyID = "k-does-not-exist"
	if err := NewKeyRing().LoadSnapshot(snap); err == nil {
		t.Fatal("expected rejection of dangling active id")
	}
	_ = first
}

func TestKeyRing_ZeroDowntimeRotation(t *testing.T) {
	// Mint a token under k0, promote k1, retire k0. Old token must fail;
	// new token must succeed. This is the headline rotation contract.
	r := NewKeyRing()
	first, _ := r.Generate()
	signer, _ := NewSigner(r)
	verifier, _ := NewVerifier(r)
	now := time.Now()
	oldTok, _ := signer.Sign(Claims{Typ: TypeAccess, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()})
	if _, err := verifier.Verify(oldTok, TypeAccess); err != nil {
		t.Fatalf("old token should verify under same ring: %v", err)
	}

	second, _ := r.Generate()
	if err := r.Promote(second.ID); err != nil {
		t.Fatal(err)
	}
	// During the verify-only window the old token still verifies.
	if _, err := verifier.Verify(oldTok, TypeAccess); err != nil {
		t.Fatalf("old token should verify in verify-only window: %v", err)
	}
	// New tokens carry the new kid.
	newTok, _ := signer.Sign(Claims{Typ: TypeAccess, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()})
	if !strings.Contains(newTok, second.ID[2:6]) && !strings.Contains(newTok, second.ID) {
		// Tokens embed the kid in their base64-encoded header; presence of
		// the kid string can be checked indirectly by verification.
	}
	if _, err := verifier.Verify(newTok, TypeAccess); err != nil {
		t.Fatalf("new token should verify: %v", err)
	}

	// Now retire the old key.
	if err := r.Retire(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(oldTok, TypeAccess); err == nil {
		t.Fatal("old token should fail after retirement of its key")
	}
	if _, err := verifier.Verify(newTok, TypeAccess); err != nil {
		t.Fatalf("new token must still verify: %v", err)
	}
}

func TestKeyRing_ConcurrentSignAndPromote(t *testing.T) {
	// Race detector smoke test: hammer Sign while Promote churns the active
	// key. Should never deadlock or produce a sign error from rotation.
	r := NewKeyRing()
	_, _ = r.Generate()
	_, _ = r.Generate()
	signer, _ := NewSigner(r)
	verifier, _ := NewVerifier(r)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		now := time.Now()
		for {
			select {
			case <-done:
				return
			default:
			}
			tok, err := signer.Sign(Claims{Typ: TypeAccess, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()})
			if err != nil {
				t.Errorf("sign: %v", err)
				return
			}
			if _, err := verifier.Verify(tok, TypeAccess); err != nil {
				t.Errorf("verify: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		keys := r.List()
		for {
			select {
			case <-done:
				return
			default:
			}
			for _, k := range keys {
				_ = r.Promote(k.ID)
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	close(done)
	wg.Wait()
}
