package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Alg names a token-signing algorithm. The default is HS256 (HMAC over
// a 32-byte secret); RS256, EdDSA, ES256, and ES384 are the asymmetric
// alternatives. The set is closed — adding a new alg requires a
// verifier branch and snapshot-format support.
type Alg string

const (
	AlgHS256 Alg = "HS256"
	AlgRS256 Alg = "RS256"
	AlgEdDSA Alg = "EdDSA"
	AlgES256 Alg = "ES256"
	AlgES384 Alg = "ES384"
)

// SupportedAlgs returns the algs Parsec can sign and verify, in the
// preferred order operators see them in CLI help. Exposed for the
// manifest layer.
func SupportedAlgs() []Alg {
	return []Alg{AlgHS256, AlgEdDSA, AlgES256, AlgES384, AlgRS256}
}

// IsAsymmetric reports whether a is one of the public-key algs. HMAC
// keys are symmetric and are NEVER exposed via the JWKS endpoint.
func (a Alg) IsAsymmetric() bool {
	switch a {
	case AlgRS256, AlgEdDSA, AlgES256, AlgES384:
		return true
	default:
		return false
	}
}

// Valid returns nil if a is one of the supported algs.
func (a Alg) Valid() error {
	switch a {
	case AlgHS256, AlgRS256, AlgEdDSA, AlgES256, AlgES384:
		return nil
	default:
		return fmt.Errorf("auth: unsupported alg %q", a)
	}
}

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

// Key is one signing key with rotation metadata. The material it
// carries depends on Alg:
//
//   - AlgHS256: Secret is the 32+ byte HMAC secret. Private/Public
//     are nil.
//   - AlgRS256: Private is an *rsa.PrivateKey, Public is its
//     *rsa.PublicKey. Secret is nil.
//   - AlgEdDSA: Private is an ed25519.PrivateKey, Public is its
//     ed25519.PublicKey. Secret is nil.
//
// Construct via KeyRing methods, never directly — the headerB64 cache
// is populated at install time.
type Key struct {
	ID        string
	Alg       Alg
	Secret    []byte
	Private   crypto.Signer
	Public    crypto.PublicKey
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

// Generate creates a fresh HS256 key (32-byte secret) and installs
// it. Kept for source compatibility with callers that predate the
// alg-aware API; prefer GenerateAlg for new code.
func (r *KeyRing) Generate() (Key, error) {
	return r.GenerateAlg(AlgHS256)
}

// GenerateAlg creates a fresh key of the requested algorithm and
// installs it as verify-only (or active if the ring was empty). For
// AlgRS256 the default modulus is 2048 bits — call GenerateRSA for
// other sizes.
func (r *KeyRing) GenerateAlg(alg Alg) (Key, error) {
	switch alg {
	case AlgHS256, "":
		id, err := newKeyID()
		if err != nil {
			return Key{}, err
		}
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return Key{}, errors.New("auth: keyring generate failed")
		}
		return r.Add(id, secret)
	case AlgEdDSA:
		return r.GenerateEd25519()
	case AlgRS256:
		return r.GenerateRSA(2048)
	case AlgES256:
		return r.GenerateECDSA(elliptic.P256())
	case AlgES384:
		return r.GenerateECDSA(elliptic.P384())
	default:
		return Key{}, alg.Valid()
	}
}

// GenerateEd25519 creates a fresh Ed25519 keypair and installs it.
func (r *KeyRing) GenerateEd25519() (Key, error) {
	id, err := newKeyID()
	if err != nil {
		return Key{}, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Key{}, fmt.Errorf("auth: ed25519 generate: %w", err)
	}
	return r.addAsymmetric(id, AlgEdDSA, priv, pub)
}

// GenerateRSA creates a fresh RSA keypair of bits modulus and installs
// it. Valid sizes are 2048, 3072, and 4096; anything else errors so an
// operator cannot accidentally land a sub-2048 key.
func (r *KeyRing) GenerateRSA(bits int) (Key, error) {
	switch bits {
	case 2048, 3072, 4096:
	default:
		return Key{}, fmt.Errorf("auth: rsa key size %d not supported (use 2048/3072/4096)", bits)
	}
	id, err := newKeyID()
	if err != nil {
		return Key{}, err
	}
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return Key{}, fmt.Errorf("auth: rsa generate: %w", err)
	}
	return r.addAsymmetric(id, AlgRS256, priv, &priv.PublicKey)
}

// Add installs an HS256 id+secret into the ring. If the ring was empty
// the new key becomes active; otherwise it joins as verify-only and
// must be Promoted before it signs.
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
		Alg:       AlgHS256,
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

// AddEd25519 installs an Ed25519 keypair into the ring. Same role
// semantics as Add.
func (r *KeyRing) AddEd25519(id string, priv ed25519.PrivateKey) (Key, error) {
	if l := len(priv); l != ed25519.PrivateKeySize {
		return Key{}, fmt.Errorf("auth: ed25519 private key wrong size: %d", l)
	}
	return r.addAsymmetric(id, AlgEdDSA, priv, priv.Public())
}

// GenerateECDSA creates a fresh ECDSA keypair on curve and installs it.
// Only P-256 (ES256) and P-384 (ES384) are supported — JOSE does not
// define a canonical signing form for other curves. P-521 is excluded
// because variable-length JOSE signatures would complicate the wire
// format; operators wanting larger keys should use RSA.
func (r *KeyRing) GenerateECDSA(curve elliptic.Curve) (Key, error) {
	alg, err := algForCurve(curve)
	if err != nil {
		return Key{}, err
	}
	id, err := newKeyID()
	if err != nil {
		return Key{}, err
	}
	priv, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		return Key{}, fmt.Errorf("auth: ecdsa generate: %w", err)
	}
	return r.addAsymmetric(id, alg, priv, &priv.PublicKey)
}

// AddECDSA installs an ECDSA private key into the ring. The curve must
// be P-256 or P-384.
func (r *KeyRing) AddECDSA(id string, priv *ecdsa.PrivateKey) (Key, error) {
	if priv == nil {
		return Key{}, errors.New("auth: nil ECDSA private key")
	}
	alg, err := algForCurve(priv.Curve)
	if err != nil {
		return Key{}, err
	}
	return r.addAsymmetric(id, alg, priv, &priv.PublicKey)
}

// algForCurve maps an EC curve to its JOSE Alg. Only NIST P-256 and
// P-384 are accepted; everything else errors so a 521-bit key cannot
// silently install as ES256.
func algForCurve(curve elliptic.Curve) (Alg, error) {
	switch curve {
	case elliptic.P256():
		return AlgES256, nil
	case elliptic.P384():
		return AlgES384, nil
	default:
		return "", fmt.Errorf("auth: ecdsa curve %s not supported (use P-256 or P-384)", curveName(curve))
	}
}

// curveName returns a human-readable curve label for error messages.
func curveName(curve elliptic.Curve) string {
	if curve == nil {
		return "<nil>"
	}
	if p := curve.Params(); p != nil {
		return p.Name
	}
	return "<unknown>"
}

// AddRSA installs an RSA private key into the ring. The caller must
// have generated a 2048+ bit key.
func (r *KeyRing) AddRSA(id string, priv *rsa.PrivateKey) (Key, error) {
	if priv == nil {
		return Key{}, errors.New("auth: nil RSA private key")
	}
	if bits := priv.N.BitLen(); bits < 2048 {
		return Key{}, fmt.Errorf("auth: rsa key %d bits too small", bits)
	}
	return r.addAsymmetric(id, AlgRS256, priv, &priv.PublicKey)
}

// addAsymmetric installs a public-key keypair. Caller must NOT hold r.mu.
func (r *KeyRing) addAsymmetric(id string, alg Alg, priv crypto.Signer, pub crypto.PublicKey) (Key, error) {
	if id == "" {
		return Key{}, errors.New("auth: key id required")
	}
	if priv == nil {
		return Key{}, errors.New("auth: nil private key")
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
		Alg:       alg,
		Private:   priv,
		Public:    pub,
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
	b, err := buildHeader(k.ID, k.Alg)
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
		entry := SnapshotKey{
			ID:        k.ID,
			Alg:       k.Alg,
			Role:      k.Role,
			CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if entry.Alg == "" {
			entry.Alg = AlgHS256
		}
		switch entry.Alg {
		case AlgHS256:
			entry.SecretHex = hex.EncodeToString(append([]byte(nil), k.Secret...))
		case AlgEdDSA:
			if priv, ok := k.Private.(ed25519.PrivateKey); ok {
				// Marshal full PKCS#8 private blob so loaders can recover
				// the key without inferring algorithm. The 32-byte seed
				// is recoverable as priv.Seed() but PKCS#8 is the JOSE
				// canonical form and reads back through x509.
				if blob, err := x509.MarshalPKCS8PrivateKey(priv); err == nil {
					entry.PrivatePEM = encodePEM("PRIVATE KEY", blob)
				}
			}
		case AlgRS256:
			if priv, ok := k.Private.(*rsa.PrivateKey); ok {
				if blob, err := x509.MarshalPKCS8PrivateKey(priv); err == nil {
					entry.PrivatePEM = encodePEM("PRIVATE KEY", blob)
				}
			}
		case AlgES256, AlgES384:
			if priv, ok := k.Private.(*ecdsa.PrivateKey); ok {
				if blob, err := x509.MarshalPKCS8PrivateKey(priv); err == nil {
					entry.PrivatePEM = encodePEM("PRIVATE KEY", blob)
				}
			}
		}
		if k.RetiredAt != nil {
			entry.RetiredAt = k.RetiredAt.UTC().Format(time.RFC3339Nano)
		}
		out.Keys = append(out.Keys, entry)
	}
	sort.Slice(out.Keys, func(i, j int) bool { return out.Keys[i].ID < out.Keys[j].ID })
	return out
}

// encodePEM returns a PEM block as a string.
func encodePEM(blockType string, data []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: data}))
}

// LoadSnapshot replaces the ring's contents with the snapshot's. Safe to
// call on a running ring — the swap is atomic from the caller's
// perspective. Legacy entries (Alg empty / empty PrivatePEM) are read
// as HS256 + secret_hex.
func (r *KeyRing) LoadSnapshot(s Snapshot) error {
	if s.ActiveKeyID == "" && len(s.Keys) > 0 {
		return errors.New("auth: snapshot has keys but no active_key_id")
	}
	next := make(map[string]*Key, len(s.Keys))
	for _, e := range s.Keys {
		createdAt, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
		if err != nil {
			return fmt.Errorf("auth: snapshot key %q: created_at: %w", e.ID, err)
		}
		alg := e.Alg
		if alg == "" {
			alg = AlgHS256
		}
		if err := alg.Valid(); err != nil {
			return fmt.Errorf("auth: snapshot key %q: %w", e.ID, err)
		}
		k := &Key{ID: e.ID, Alg: alg, Role: e.Role, CreatedAt: createdAt}
		switch alg {
		case AlgHS256:
			secret, err := hex.DecodeString(e.SecretHex)
			if err != nil {
				return fmt.Errorf("auth: snapshot key %q: secret_hex: %w", e.ID, err)
			}
			if len(secret) < 32 {
				return fmt.Errorf("auth: snapshot key %q: secret too short", e.ID)
			}
			k.Secret = secret
		case AlgRS256, AlgEdDSA, AlgES256, AlgES384:
			if e.PrivatePEM == "" {
				return fmt.Errorf("auth: snapshot key %q: missing private_pem", e.ID)
			}
			priv, pub, err := decodePrivatePEM(e.PrivatePEM, alg)
			if err != nil {
				return fmt.Errorf("auth: snapshot key %q: %w", e.ID, err)
			}
			k.Private = priv
			k.Public = pub
		}
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

// decodePrivatePEM extracts the (signer, public) pair from a PKCS#8
// PEM blob and validates it against the declared alg.
func decodePrivatePEM(s string, alg Alg) (crypto.Signer, crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, nil, errors.New("private_pem: no PEM block")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("private_pem parse: %w", err)
	}
	switch alg {
	case AlgEdDSA:
		ed, ok := priv.(ed25519.PrivateKey)
		if !ok {
			return nil, nil, fmt.Errorf("private_pem: expected Ed25519, got %T", priv)
		}
		return ed, ed.Public(), nil
	case AlgRS256:
		rk, ok := priv.(*rsa.PrivateKey)
		if !ok {
			return nil, nil, fmt.Errorf("private_pem: expected RSA, got %T", priv)
		}
		return rk, &rk.PublicKey, nil
	case AlgES256, AlgES384:
		ek, ok := priv.(*ecdsa.PrivateKey)
		if !ok {
			return nil, nil, fmt.Errorf("private_pem: expected ECDSA, got %T", priv)
		}
		// Defend against an operator hand-editing a snapshot to swap
		// curves: the curve on disk must match the declared alg.
		expected, err := algForCurve(ek.Curve)
		if err != nil {
			return nil, nil, fmt.Errorf("private_pem: %w", err)
		}
		if expected != alg {
			return nil, nil, fmt.Errorf("private_pem: alg %s but curve %s", alg, expected)
		}
		return ek, &ek.PublicKey, nil
	default:
		return nil, nil, fmt.Errorf("private_pem: alg %s not asymmetric", alg)
	}
}

// buildHeaderInto fills k.headerB64. Defined here (not on KeyRing) so
// LoadSnapshot can use it before installing the key.
func buildHeaderInto(k *Key) error {
	b, err := buildHeader(k.ID, k.Alg)
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

// SnapshotKey is one persisted key entry. Algorithm-specific material:
//
//   - HS256: SecretHex is the 32+ byte HMAC secret (legacy entries
//     without Alg are also read as HS256).
//   - RS256 / EdDSA: PrivatePEM is a PKCS#8 PEM blob.
type SnapshotKey struct {
	ID         string `json:"id"`
	Alg        Alg    `json:"alg,omitempty"`
	SecretHex  string `json:"secret_hex,omitempty"`
	PrivatePEM string `json:"private_pem,omitempty"`
	Role       Role   `json:"role"`
	CreatedAt  string `json:"created_at"`
	RetiredAt  string `json:"retired_at,omitempty"`
}
