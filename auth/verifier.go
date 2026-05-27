package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// Verifier validates Parsec JWTs against a KeyRing. The ring is followed
// by reference, so a reload that swaps the ring's contents takes effect
// on the next call.
type Verifier struct {
	ring   *KeyRing
	Clock  func() time.Time
	Leeway time.Duration
	// OnVerify, when non-nil, is invoked once per Verify call with the
	// token type that was checked (or the empty string when the token
	// failed to parse before the typ claim could be read), the expected
	// type (may be empty), and the resulting error (nil on success).
	// Used by the metrics layer to record verifications by type+result
	// without coupling the auth package to prometheus.
	OnVerify func(parsed Type, expected Type, err error)
}

// NewVerifier returns a Verifier bound to ring.
func NewVerifier(ring *KeyRing) (*Verifier, error) {
	if ring == nil {
		return nil, errors.New("auth: NewVerifier requires a KeyRing")
	}
	return &Verifier{ring: ring, Clock: time.Now}, nil
}

// Verify parses token, validates the header (must include kid, must use
// HS256+JWT), looks up the key in the ring, verifies the signature, and
// checks expiry. When expected != "" it also enforces the typ claim.
func (v *Verifier) Verify(token string, expected Type) (Claims, error) {
	claims, err := v.verify(token, expected)
	if v.OnVerify != nil {
		v.OnVerify(claims.Typ, expected, err)
	}
	return claims, err
}

func (v *Verifier) verify(token string, expected Type) (Claims, error) {
	if v.Clock == nil {
		v.Clock = time.Now
	}
	headerB64, payloadB64, sigB64, err := splitToken(token)
	if err != nil {
		return Claims{}, err
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(headerB64)
	if err != nil {
		return Claims{}, ErrMalformedToken
	}
	var h joseHeader
	if err := json.Unmarshal(headerBytes, &h); err != nil {
		return Claims{}, ErrMalformedToken
	}
	if h.Alg != "HS256" || h.Typ != "JWT" {
		return Claims{}, ErrUnsupportedAlg
	}
	if h.Kid == "" {
		return Claims{}, ErrMalformedToken
	}
	key, err := v.ring.Get(h.Kid)
	if err != nil {
		// Unknown or retired key: treat as a bad signature. Don't leak
		// which case applies (defends against probing).
		return Claims{}, ErrInvalidSignature
	}
	// Recompute signature using the precomputed header bytes to avoid
	// any chance of canonicalization drift.
	mac := hmac.New(sha256.New, key.Secret)
	mac.Write([]byte(headerB64 + "." + payloadB64))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return Claims{}, ErrMalformedToken
	}
	if !hmac.Equal(want, got) {
		return Claims{}, ErrInvalidSignature
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return Claims{}, ErrMalformedToken
	}
	var c Claims
	if err := json.Unmarshal(payloadBytes, &c); err != nil {
		return Claims{}, ErrMalformedToken
	}
	if err := c.Typ.Valid(); err != nil {
		return Claims{}, ErrMalformedToken
	}
	now := v.Clock()
	if now.Sub(time.Unix(c.Exp, 0)) > v.Leeway {
		return Claims{}, ErrExpired
	}
	if time.Unix(c.Iat, 0).Sub(now) > v.Leeway {
		return Claims{}, ErrNotYetValid
	}
	if expected != "" && c.Typ != expected {
		return Claims{}, ErrTypeMismatch
	}
	return c, nil
}
