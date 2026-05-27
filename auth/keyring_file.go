package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeyringFileName is the conventional file name inside the state dir.
const KeyringFileName = "keyring.json"

// SaveKeyRing writes the ring's snapshot to path, atomically. The file is
// created with mode 0600; the parent directory is created with mode 0700
// if missing.
func SaveKeyRing(path string, r *KeyRing) error {
	if path == "" {
		return errors.New("auth: SaveKeyRing: path required")
	}
	if r == nil {
		return errors.New("auth: SaveKeyRing: nil ring")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("auth: create state dir: %w", err)
	}
	snap := r.Snapshot()
	snap.FormatVersion = "1"
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("auth: write temp keyring: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("auth: rename keyring into place: %w", err)
	}
	return nil
}

// LoadKeyRing reads path into a fresh ring. If the file does not exist,
// returns (nil, os.ErrNotExist) so the caller can decide whether to
// bootstrap.
func LoadKeyRing(path string) (*KeyRing, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("auth: parse keyring: %w", err)
	}
	if snap.FormatVersion != "" && snap.FormatVersion != "1" {
		return nil, fmt.Errorf("auth: keyring format_version %q is not supported", snap.FormatVersion)
	}
	r := NewKeyRing()
	if err := r.LoadSnapshot(snap); err != nil {
		return nil, err
	}
	return r, nil
}

// ReloadInto re-reads path into the existing ring, swapping its contents
// atomically. The pointer identity is preserved so any Signer / Verifier
// already bound to ring picks up the new keys.
func ReloadInto(path string, ring *KeyRing) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return fmt.Errorf("auth: parse keyring: %w", err)
	}
	if snap.FormatVersion != "" && snap.FormatVersion != "1" {
		return fmt.Errorf("auth: keyring format_version %q is not supported", snap.FormatVersion)
	}
	return ring.LoadSnapshot(snap)
}

// EnsureKeyRing returns a usable KeyRing for path. If the file exists, it
// is loaded. If not, a fresh ring with one active key is generated and
// saved. The bool return is true when bootstrap happened.
func EnsureKeyRing(path string) (*KeyRing, bool, error) {
	r, err := LoadKeyRing(path)
	if err == nil {
		return r, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	r = NewKeyRing()
	if _, err := r.Generate(); err != nil {
		return nil, false, err
	}
	if err := SaveKeyRing(path, r); err != nil {
		return nil, false, err
	}
	return r, true, nil
}
