package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// joseHeader is the typed JOSE header parsec writes. Field order matches
// the JSON output — encoding/json walks struct fields in declaration
// order, so signing is byte-stable.
type joseHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// buildHeader returns the base64url-without-padding encoding of the JOSE
// header for kid + alg. Used by KeyRing to precompute per-key headers
// at install time. An empty alg defaults to HS256 so old call sites that
// know they hold HMAC keys still compile.
func buildHeader(kid string, alg Alg) (string, error) {
	if kid == "" {
		return "", errors.New("auth: kid required")
	}
	if alg == "" {
		alg = AlgHS256
	}
	if err := alg.Valid(); err != nil {
		return "", err
	}
	b, err := json.Marshal(joseHeader{Alg: string(alg), Kid: kid, Typ: "JWT"})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Signer produces signed Parsec JWTs from Claims using a KeyRing's active
// key.
type Signer struct {
	ring *KeyRing
}

// NewSigner returns a Signer bound to ring. The ring must have at least
// one active key — checked lazily at sign time.
func NewSigner(ring *KeyRing) (*Signer, error) {
	if ring == nil {
		return nil, errors.New("auth: NewSigner requires a KeyRing")
	}
	return &Signer{ring: ring}, nil
}

// Sign serializes claims and signs them with the keyring's currently-active
// key. The returned token's header embeds that key's kid and alg.
func (s *Signer) Sign(c Claims) (string, error) {
	if err := c.Typ.Valid(); err != nil {
		return "", err
	}
	if c.Iat == 0 || c.Exp == 0 {
		return "", errors.New("auth: iat and exp are required")
	}
	if c.Exp <= c.Iat {
		return "", errors.New("auth: exp must be after iat")
	}
	active, err := s.ring.Active()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := active.headerB64 + "." + payloadB64
	sigBytes, err := signWith(active, []byte(signingInput))
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sigBytes), nil
}

// signWith returns the raw signature bytes produced by k over msg.
// Alg-specific:
//
//   - HS256: HMAC-SHA256 with k.Secret.
//   - RS256: RSASSA-PKCS1-v1_5 over SHA-256.
//   - EdDSA: Ed25519 over the raw msg (Ed25519 hashes internally).
func signWith(k Key, msg []byte) ([]byte, error) {
	alg := k.Alg
	if alg == "" {
		alg = AlgHS256
	}
	switch alg {
	case AlgHS256:
		mac := hmac.New(sha256.New, k.Secret)
		mac.Write(msg)
		return mac.Sum(nil), nil
	case AlgRS256:
		priv, ok := k.Private.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("auth: key %q has alg %s but non-RSA private key", k.ID, alg)
		}
		sum := sha256.Sum256(msg)
		return rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	case AlgEdDSA:
		priv, ok := k.Private.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("auth: key %q has alg %s but non-Ed25519 private key", k.ID, alg)
		}
		return ed25519.Sign(priv, msg), nil
	case AlgES256, AlgES384:
		priv, ok := k.Private.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("auth: key %q has alg %s but non-ECDSA private key", k.ID, alg)
		}
		digest, coordSize, err := ecdsaDigest(alg, msg)
		if err != nil {
			return nil, err
		}
		r, s, err := ecdsa.Sign(rand.Reader, priv, digest)
		if err != nil {
			return nil, fmt.Errorf("auth: ecdsa sign: %w", err)
		}
		// JOSE encoding: fixed-width big-endian r||s (RFC 7515 §3.4).
		// Pad each component with leading zeros to coordSize bytes.
		out := make([]byte, 2*coordSize)
		rb := r.Bytes()
		sb := s.Bytes()
		copy(out[coordSize-len(rb):coordSize], rb)
		copy(out[2*coordSize-len(sb):], sb)
		return out, nil
	default:
		return nil, fmt.Errorf("auth: cannot sign with alg %q", alg)
	}
}

// ecdsaDigest hashes msg with the alg-appropriate digest and returns
// the hashed bytes plus the curve's coordinate size (32 for P-256,
// 48 for P-384). The coord size is the per-component width of the
// fixed-width r||s JOSE signature.
func ecdsaDigest(alg Alg, msg []byte) ([]byte, int, error) {
	switch alg {
	case AlgES256:
		sum := sha256.Sum256(msg)
		return sum[:], 32, nil
	case AlgES384:
		sum := sha512.Sum384(msg)
		return sum[:], 48, nil
	default:
		return nil, 0, fmt.Errorf("auth: ecdsaDigest: unsupported alg %s", alg)
	}
}

// splitToken returns the three compact-serialization parts or an error.
func splitToken(token string) (header, payload, sig string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", "", ErrMalformedToken
	}
	return parts[0], parts[1], parts[2], nil
}
