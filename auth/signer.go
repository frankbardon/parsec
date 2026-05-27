package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
// header for the given kid. Used by KeyRing to precompute per-key headers
// at install time.
func buildHeader(kid string) (string, error) {
	if kid == "" {
		return "", errors.New("auth: kid required")
	}
	b, err := json.Marshal(joseHeader{Alg: "HS256", Kid: kid, Typ: "JWT"})
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
// key. The returned token's header embeds that key's kid.
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
	mac := hmac.New(sha256.New, active.Secret)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig, nil
}

// splitToken returns the three compact-serialization parts or an error.
func splitToken(token string) (header, payload, sig string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", "", ErrMalformedToken
	}
	return parts[0], parts[1], parts[2], nil
}
