package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
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
	if h.Typ != "JWT" {
		return Claims{}, ErrUnsupportedAlg
	}
	if h.Kid == "" {
		return Claims{}, ErrMalformedToken
	}
	hdrAlg := Alg(h.Alg)
	if err := hdrAlg.Valid(); err != nil {
		return Claims{}, ErrUnsupportedAlg
	}
	key, err := v.ring.Get(h.Kid)
	if err != nil {
		// Unknown or retired key: treat as a bad signature. Don't leak
		// which case applies (defends against probing).
		return Claims{}, ErrInvalidSignature
	}
	// Header-declared alg must match the alg of the key it points to:
	// no swapping (e.g. an attacker presenting an HMAC of an RSA key's
	// public material as if it were the original RS256 signature).
	keyAlg := key.Alg
	if keyAlg == "" {
		keyAlg = AlgHS256
	}
	if hdrAlg != keyAlg {
		return Claims{}, ErrUnsupportedAlg
	}
	got, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return Claims{}, ErrMalformedToken
	}
	if err := verifyWith(key, []byte(headerB64+"."+payloadB64), got); err != nil {
		return Claims{}, err
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

// verifyWith checks sig against msg under k. Returns ErrInvalidSignature
// on any verification failure; any non-nil error from RSA/Ed25519 maps
// to ErrInvalidSignature so callers cannot distinguish failure modes.
func verifyWith(k Key, msg, sig []byte) error {
	alg := k.Alg
	if alg == "" {
		alg = AlgHS256
	}
	switch alg {
	case AlgHS256:
		mac := hmac.New(sha256.New, k.Secret)
		mac.Write(msg)
		if !hmac.Equal(mac.Sum(nil), sig) {
			return ErrInvalidSignature
		}
		return nil
	case AlgRS256:
		pub, ok := k.Public.(*rsa.PublicKey)
		if !ok {
			return ErrInvalidSignature
		}
		sum := sha256.Sum256(msg)
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
			return ErrInvalidSignature
		}
		return nil
	case AlgEdDSA:
		pub, ok := k.Public.(ed25519.PublicKey)
		if !ok {
			return ErrInvalidSignature
		}
		if !ed25519.Verify(pub, msg, sig) {
			return ErrInvalidSignature
		}
		return nil
	case AlgES256, AlgES384:
		pub, ok := k.Public.(*ecdsa.PublicKey)
		if !ok {
			return ErrInvalidSignature
		}
		digest, coordSize, err := ecdsaDigest(alg, msg)
		if err != nil {
			return ErrInvalidSignature
		}
		// JOSE r||s is fixed-width per RFC 7515 §3.4; any other length is
		// invalid even if the underlying signature would verify.
		if len(sig) != 2*coordSize {
			return ErrInvalidSignature
		}
		// Reject signatures whose curve doesn't match the key (e.g. a
		// P-256 sig presented for a P-384 key would otherwise fall to
		// ecdsa.Verify which clamps silently). The coordSize check above
		// already catches the cross-curve case, but be defensive.
		if expected, err := algForCurve(pub.Curve); err != nil || expected != alg {
			return ErrInvalidSignature
		}
		r := new(big.Int).SetBytes(sig[:coordSize])
		s := new(big.Int).SetBytes(sig[coordSize:])
		if !ecdsa.Verify(pub, digest, r, s) {
			return ErrInvalidSignature
		}
		return nil
	default:
		return fmt.Errorf("auth: cannot verify alg %q", alg)
	}
}
