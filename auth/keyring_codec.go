package auth

import (
	"encoding/json"
	"fmt"
)

// KeyRingFormatVersion is the snapshot format a fresh encode writes.
// Exported so a KeyRingStore living outside this module can tag what it
// persists with the same version the in-tree stores do.
const KeyRingFormatVersion = keyringFormatVersion

// EncodeKeyRing marshals r's snapshot, stamped with the current format
// version. Every KeyRingStore that persists the ring as an opaque blob
// should encode through here rather than marshalling Snapshot directly:
// forgetting the version stamp writes a snapshot that loaders treat as
// legacy v1.
func EncodeKeyRing(r *KeyRing) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("auth: EncodeKeyRing: nil ring")
	}
	snap := r.Snapshot()
	snap.FormatVersion = keyringFormatVersion
	return json.Marshal(snap)
}

// DecodeKeyRing parses a snapshot written by EncodeKeyRing into a fresh
// ring, rejecting format versions this build does not understand.
func DecodeKeyRing(body []byte) (*KeyRing, error) {
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return nil, fmt.Errorf("auth: decode keyring: %w", err)
	}
	if !supportedFormatVersion(snap.FormatVersion) {
		return nil, fmt.Errorf("auth: keyring format_version %q is not supported", snap.FormatVersion)
	}
	r := NewKeyRing()
	if err := r.LoadSnapshot(snap); err != nil {
		return nil, err
	}
	return r, nil
}
