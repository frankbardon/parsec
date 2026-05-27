package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Role is a key's lifecycle position in a KeyRing.
type Role string

const (
	// RoleActive is the signing key. Exactly one key in a ring holds this
	// role at a time. New tokens are minted with this key's kid.
	RoleActive Role = "active"
	// RoleVerifyOnly accepts existing tokens but does not sign new ones.
	// A key transitions to verify-only when superseded by Promote, or when
	// added via Add (which never installs as active).
	RoleVerifyOnly Role = "verify-only"
	// RoleRetired is a deleted key. Retired keys are dropped from the next
	// snapshot and stop verifying immediately.
	RoleRetired Role = "retired"
)

// Key is one HMAC secret with rotation metadata. Construct via KeyRing,
// never directly.
type Key struct {
	ID        string
	Secret    []byte
	Role      Role
	CreatedAt time.Time
	RetiredAt *time.Time

	// headerB64 caches the precomputed base64url JOSE header for this key.
	// Set by KeyRing.installHeader once the key joins the ring.
	headerB64 string
}

// KeyRing is the live set of HMAC keys parsec uses to sign and verify
// tokens. Exactly one key holds RoleActive. The ring is safe for
// concurrent use.
type KeyRing struct {
	mu     sync.RWMutex
	byID   map[string]*Key
	active string // id of the active key, "" before first Add
}

// NewKeyRing returns an empty KeyRing.
func NewKeyRing() *KeyRing {
	return &KeyRing{byID: make(map[string]*Key)}
}

// NewKeyRingFromSecret seeds a ring with one active key whose Secret is
// the supplied bytes. Convenient for tests that want a stable HMAC
// across a parsec.New / recreate pair without using a state directory.
func NewKeyRingFromSecret(secret []byte) (*KeyRing, error) {
	r := NewKeyRing()
	k, err := r.Generate()
	if err != nil {
		return nil, err
	}
	// Replace the generated secret with the provided one.
	r.mu.Lock()
	r.byID[k.ID].Secret = secret
	if err := r.installHeader(r.byID[k.ID]); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	r.mu.Unlock()
	return r, nil
}

// Active returns a copy of the active key. Errors if no key is active.
func (r *KeyRing) Active() (Key, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.active == "" {
		return Key{}, errors.New("auth: keyring has no active key")
	}
	k := *r.byID[r.active]
	return k, nil
}

// ActiveID returns the active key's ID, or "" if there is none.
func (r *KeyRing) ActiveID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active
}

// Get returns a copy of the key with id, or an error if no such key
// exists OR the key is retired (callers should treat both the same).
func (r *KeyRing) Get(id string) (Key, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.byID[id]
	if !ok {
		return Key{}, fmt.Errorf("auth: unknown key id %q", id)
	}
	if k.Role == RoleRetired {
		return Key{}, fmt.Errorf("auth: key %q is retired", id)
	}
	out := *k
	return out, nil
}

// List returns a snapshot of every key in the ring, sorted by CreatedAt.
// Retired keys are included so the operator can audit recent deletions.
func (r *KeyRing) List() []Key {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Key, 0, len(r.byID))
	for _, k := range r.byID {
		out = append(out, *k)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Generate creates a fresh 32-byte secret, installs it as a new key in the
// ring, and returns a copy. If the ring was empty the new key becomes
// active; otherwise it joins as verify-only.
func (r *KeyRing) Generate() (Key, error) {
	id, err := newKeyID()
	if err != nil {
		return Key{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Key{}, errors.New("auth: keyring generate failed")
	}
	return r.Add(id, secret)
}

// Add installs id+secret into the ring. If the ring was empty the new key
// becomes active; otherwise it joins as verify-only and must be Promoted
// before it signs.
func (r *KeyRing) Add(id string, secret []byte) (Key, error) {
	if id == "" {
		return Key{}, errors.New("auth: key id required")
	}
	if len(secret) < 32 {
		return Key{}, errors.New("auth: key secret must be at least 32 bytes")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[id]; exists {
		return Key{}, fmt.Errorf("auth: key id %q already exists", id)
	}
	role := RoleVerifyOnly
	if len(r.byID) == 0 {
		role = RoleActive
	}
	k := &Key{
		ID:        id,
		Secret:    append([]byte(nil), secret...),
		Role:      role,
		CreatedAt: time.Now().UTC(),
	}
	if err := r.installHeader(k); err != nil {
		return Key{}, err
	}
	r.byID[id] = k
	if role == RoleActive {
		r.active = id
	}
	out := *k
	return out, nil
}

// Promote makes id the active key. The previously-active key (if any)
// transitions to verify-only. Promoting an unknown or retired key errors.
func (r *KeyRing) Promote(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("auth: unknown key id %q", id)
	}
	if k.Role == RoleRetired {
		return fmt.Errorf("auth: cannot promote retired key %q", id)
	}
	if r.active != "" && r.active != id {
		r.byID[r.active].Role = RoleVerifyOnly
	}
	k.Role = RoleActive
	r.active = id
	return nil
}

// Retire marks id as retired. Retiring the active key errors — Promote
// another key first. Retiring an already-retired key is a no-op.
func (r *KeyRing) Retire(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("auth: unknown key id %q", id)
	}
	if k.Role == RoleRetired {
		return nil
	}
	if id == r.active {
		return fmt.Errorf("auth: cannot retire active key %q; promote another first", id)
	}
	now := time.Now().UTC()
	k.Role = RoleRetired
	k.RetiredAt = &now
	return nil
}

// installHeader pre-computes the per-key base64url JOSE header. Caller
// must hold r.mu in write mode.
func (r *KeyRing) installHeader(k *Key) error {
	b, err := buildHeader(k.ID)
	if err != nil {
		return err
	}
	k.headerB64 = b
	return nil
}

// newKeyID returns a short slug usable as a JWT kid: 12 lowercase hex
// characters prefixed with "k-". Probability of collision in any realistic
// ring is negligible.
func newKeyID() (string, error) {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", errors.New("auth: key id generation failed")
	}
	return "k-" + hex.EncodeToString(buf[:]), nil
}

// Snapshot returns a deep copy of the ring suitable for persistence or
// transfer. Excludes retired keys.
func (r *KeyRing) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := Snapshot{ActiveKeyID: r.active}
	for _, k := range r.byID {
		if k.Role == RoleRetired {
			continue
		}
		secretCopy := append([]byte(nil), k.Secret...)
		entry := SnapshotKey{
			ID:        k.ID,
			SecretHex: hex.EncodeToString(secretCopy),
			Role:      k.Role,
			CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if k.RetiredAt != nil {
			entry.RetiredAt = k.RetiredAt.UTC().Format(time.RFC3339Nano)
		}
		out.Keys = append(out.Keys, entry)
	}
	sort.Slice(out.Keys, func(i, j int) bool { return out.Keys[i].ID < out.Keys[j].ID })
	return out
}

// LoadSnapshot replaces the ring's contents with the snapshot's. Safe to
// call on a running ring — the swap is atomic from the caller's
// perspective.
func (r *KeyRing) LoadSnapshot(s Snapshot) error {
	if s.ActiveKeyID == "" && len(s.Keys) > 0 {
		return errors.New("auth: snapshot has keys but no active_key_id")
	}
	next := make(map[string]*Key, len(s.Keys))
	for _, e := range s.Keys {
		secret, err := hex.DecodeString(e.SecretHex)
		if err != nil {
			return fmt.Errorf("auth: snapshot key %q: secret_hex: %w", e.ID, err)
		}
		if len(secret) < 32 {
			return fmt.Errorf("auth: snapshot key %q: secret too short", e.ID)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
		if err != nil {
			return fmt.Errorf("auth: snapshot key %q: created_at: %w", e.ID, err)
		}
		k := &Key{ID: e.ID, Secret: secret, Role: e.Role, CreatedAt: createdAt}
		if e.RetiredAt != "" {
			t, err := time.Parse(time.RFC3339Nano, e.RetiredAt)
			if err != nil {
				return fmt.Errorf("auth: snapshot key %q: retired_at: %w", e.ID, err)
			}
			k.RetiredAt = &t
		}
		if err := buildHeaderInto(k); err != nil {
			return err
		}
		next[e.ID] = k
	}
	if _, ok := next[s.ActiveKeyID]; s.ActiveKeyID != "" && !ok {
		return fmt.Errorf("auth: snapshot active_key_id %q not present in keys", s.ActiveKeyID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID = next
	r.active = s.ActiveKeyID
	return nil
}

// buildHeaderInto fills k.headerB64. Defined here (not on KeyRing) so
// LoadSnapshot can use it before installing the key.
func buildHeaderInto(k *Key) error {
	b, err := buildHeader(k.ID)
	if err != nil {
		return err
	}
	k.headerB64 = b
	return nil
}

// Snapshot is the serializable view of a KeyRing.
type Snapshot struct {
	FormatVersion string        `json:"format_version"`
	ActiveKeyID   string        `json:"active_key_id"`
	Keys          []SnapshotKey `json:"keys"`
}

// SnapshotKey is one persisted key entry.
type SnapshotKey struct {
	ID        string `json:"id"`
	SecretHex string `json:"secret_hex"`
	Role      Role   `json:"role"`
	CreatedAt string `json:"created_at"`
	RetiredAt string `json:"retired_at,omitempty"`
}
