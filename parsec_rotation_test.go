package parsec

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
)

// TestRotation_ZeroDowntimeFlow walks the full operator procedure:
//
//  1. Boot with state dir → bootstrap k0.
//  2. Mint a mgmt token under k0; verify it.
//  3. Generate k1 (verify-only).
//  4. Promote k1 → previous active demotes.
//  5. Mint a NEW mgmt under k1 (this is the operator's new bearer).
//  6. Old k0-signed mgmt still verifies (verify-only window).
//  7. Retire k0; k0-signed tokens now fail; k1-signed tokens still succeed.
//
// No restarts, no broker downtime.
func TestRotation_ZeroDowntimeFlow(t *testing.T) {
	dir := t.TempDir()
	p, err := New(Options{StateDir: dir, KeyringPollInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
	}()
	time.Sleep(75 * time.Millisecond)

	// Sanity: file exists with the bootstrap key.
	if _, err := auth.LoadKeyRing(filepath.Join(dir, auth.KeyringFileName)); err != nil {
		t.Fatalf("bootstrap keyring should exist: %v", err)
	}

	// 2: mint mgmt under k0.
	k0 := p.KeyRing().ActiveID()
	mgmtK0, _, err := p.Issuer().IssueMgmt("op", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Verifier().Verify(mgmtK0, auth.TypeMgmt); err != nil {
		t.Fatalf("k0 mgmt should verify: %v", err)
	}

	// 3: generate k1 verify-only.
	k1, err := p.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if k1.Role != auth.RoleVerifyOnly {
		t.Fatalf("k1 should be verify-only, got %s", k1.Role)
	}
	if p.KeyRing().ActiveID() != k0 {
		t.Fatal("active should still be k0 before promote")
	}

	// 4: promote k1.
	if err := p.PromoteKey(k1.ID); err != nil {
		t.Fatal(err)
	}
	if p.KeyRing().ActiveID() != k1.ID {
		t.Fatalf("active should be k1 after promote, got %s", p.KeyRing().ActiveID())
	}

	// 5: mint NEW mgmt under k1.
	mgmtK1, _, err := p.Issuer().IssueMgmt("op", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Verifier().Verify(mgmtK1, auth.TypeMgmt); err != nil {
		t.Fatalf("k1 mgmt should verify: %v", err)
	}

	// 6: old mgmt still verifies (k0 is verify-only).
	if _, err := p.Verifier().Verify(mgmtK0, auth.TypeMgmt); err != nil {
		t.Fatalf("k0 mgmt should still verify in verify-only window: %v", err)
	}

	// 7: retire k0.
	if err := p.RetireKey(k0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Verifier().Verify(mgmtK0, auth.TypeMgmt); err == nil {
		t.Fatal("k0 mgmt should fail after retirement")
	}
	if _, err := p.Verifier().Verify(mgmtK1, auth.TypeMgmt); err != nil {
		t.Fatalf("k1 mgmt must still verify: %v", err)
	}
}

func TestRotation_CannotRetireActive(t *testing.T) {
	dir := t.TempDir()
	p, err := New(Options{StateDir: dir, KeyringPollInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	time.Sleep(75 * time.Millisecond)

	if err := p.RetireKey(p.KeyRing().ActiveID()); err == nil {
		t.Fatal("expected retiring the active key to fail")
	}
}

func TestRotation_FilePersistence(t *testing.T) {
	dir := t.TempDir()
	p, err := New(Options{StateDir: dir, KeyringPollInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	k0 := p.KeyRing().ActiveID()
	k1, _ := p.GenerateKey()
	if err := p.PromoteKey(k1.ID); err != nil {
		t.Fatal(err)
	}

	// Re-read the keyring from disk and confirm active id survived.
	r, err := auth.LoadKeyRing(filepath.Join(dir, auth.KeyringFileName))
	if err != nil {
		t.Fatal(err)
	}
	if r.ActiveID() != k1.ID {
		t.Fatalf("on-disk active should be k1, got %s", r.ActiveID())
	}
	if _, err := r.Get(k0); err != nil {
		t.Fatalf("k0 should still be on disk: %v", err)
	}
}

func TestRotation_BootstrapMintsKey(t *testing.T) {
	dir := t.TempDir()
	p, err := New(Options{StateDir: dir, KeyringPollInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyRing().ActiveID() == "" {
		t.Fatal("expected an active key after bootstrap")
	}
	if _, err := auth.LoadKeyRing(filepath.Join(dir, auth.KeyringFileName)); err != nil {
		t.Fatalf("bootstrap should create keyring file: %v", err)
	}
}
