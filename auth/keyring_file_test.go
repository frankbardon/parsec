package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSaveLoadKeyRing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KeyringFileName)
	r := NewKeyRing()
	first, _ := r.Generate()
	second, _ := r.Generate()
	if err := r.Promote(second.ID); err != nil {
		t.Fatal(err)
	}
	if err := SaveKeyRing(path, r); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms, got %o", info.Mode().Perm())
	}

	loaded, err := LoadKeyRing(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := loaded.Active(); got.ID != second.ID {
		t.Fatalf("active mismatch after load: %s", got.ID)
	}
	if _, err := loaded.Get(first.ID); err != nil {
		t.Fatalf("first key should still verify: %v", err)
	}
}

func TestEnsureKeyRing_Bootstrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KeyringFileName)
	r, bootstrapped, err := EnsureKeyRing(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap on first call")
	}
	if r.ActiveID() == "" {
		t.Fatal("expected an active key after bootstrap")
	}
	// Second call must NOT bootstrap.
	r2, bootstrapped, err := EnsureKeyRing(path)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrapped {
		t.Fatal("expected no bootstrap on second call")
	}
	if r2.ActiveID() != r.ActiveID() {
		t.Fatalf("active id should survive reload: %s vs %s", r.ActiveID(), r2.ActiveID())
	}
}

func TestLoadKeyRing_MissingFile(t *testing.T) {
	_, err := LoadKeyRing(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestReloadInto_SwapsContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KeyringFileName)
	r := NewKeyRing()
	first, _ := r.Generate()
	if err := SaveKeyRing(path, r); err != nil {
		t.Fatal(err)
	}

	// Rotate on a side ring, save, then reload into the running ring.
	side := NewKeyRing()
	if err := side.LoadSnapshot(r.Snapshot()); err != nil {
		t.Fatal(err)
	}
	second, _ := side.Generate()
	if err := side.Promote(second.ID); err != nil {
		t.Fatal(err)
	}
	if err := SaveKeyRing(path, side); err != nil {
		t.Fatal(err)
	}

	if err := ReloadInto(path, r); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Active()
	if got.ID != second.ID {
		t.Fatalf("ring active should be second after reload: %s vs %s", got.ID, second.ID)
	}
	// Old verifier path still works on the original key (verify-only).
	if _, err := r.Get(first.ID); err != nil {
		t.Fatalf("first key should still verify after reload: %v", err)
	}
}

func TestWatchKeyRingFile_RealRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KeyringFileName)
	r := NewKeyRing()
	_, _ = r.Generate()
	if err := SaveKeyRing(path, r); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	reloads := []string{}
	go WatchKeyRingFile(ctx, path, r, 20*time.Millisecond,
		func(activeID string) {
			mu.Lock()
			defer mu.Unlock()
			reloads = append(reloads, activeID)
		},
		nil,
	)

	// Make sure the next save bumps mtime even on filesystems that round
	// to seconds.
	time.Sleep(50 * time.Millisecond)

	side := NewKeyRing()
	_ = side.LoadSnapshot(r.Snapshot())
	second, _ := side.Generate()
	_ = side.Promote(second.ID)
	if err := SaveKeyRing(path, side); err != nil {
		t.Fatal(err)
	}
	// Bump mtime explicitly to defeat second-rounding.
	future := time.Now().Add(time.Second)
	_ = os.Chtimes(path, future, future)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(reloads)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reloads) == 0 {
		t.Fatal("watcher never fired")
	}
	if reloads[len(reloads)-1] != second.ID {
		t.Fatalf("last reload should report %s, got %s", second.ID, reloads[len(reloads)-1])
	}
}
