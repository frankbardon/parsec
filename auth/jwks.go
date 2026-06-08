package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// JWKSHandler returns an http.Handler that serves the asymmetric
// public keys in ring as a JWKS document (RFC 7517). HMAC keys are
// NEVER exposed — they are shared secrets, not verifying material a
// third party should hold. Retired keys are omitted; the active key
// is included.
//
// The handler is read-only and unauthenticated by design: a JWKS
// endpoint must be reachable by any party that needs to verify
// tokens the operator has issued. Mount it on an internet-reachable
// path only when the keys it advertises are intended for external
// verification.
func JWKSHandler(ring *KeyRing) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/jwk-set+json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		out := jwksFromRing(ring)
		_ = json.NewEncoder(w).Encode(out)
	})
}

// JWKS is the wire shape produced by JWKSHandler.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWK is one entry. Only the fields relevant to the supported algs
// are emitted; conformant consumers ignore unknown fields.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	// EdDSA (OKP) and ECDSA (EC)
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	// ECDSA y coordinate (EC only — OKP keys are single-coord)
	Y string `json:"y,omitempty"`
	// RSA
	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`
}

func jwksFromRing(ring *KeyRing) JWKS {
	out := JWKS{Keys: []JWK{}}
	if ring == nil {
		return out
	}
	for _, k := range ring.List() {
		if k.Role == RoleRetired {
			continue
		}
		jwk, ok := keyToJWK(k)
		if !ok {
			continue
		}
		out.Keys = append(out.Keys, jwk)
	}
	return out
}

func keyToJWK(k Key) (JWK, bool) {
	switch k.Alg {
	case AlgEdDSA:
		pub, ok := k.Public.(ed25519.PublicKey)
		if !ok {
			return JWK{}, false
		}
		return JWK{
			Kty: "OKP",
			Kid: k.ID,
			Alg: string(AlgEdDSA),
			Use: "sig",
			Crv: "Ed25519",
			X:   base64.RawURLEncoding.EncodeToString(pub),
		}, true
	case AlgRS256:
		pub, ok := k.Public.(*rsa.PublicKey)
		if !ok {
			return JWK{}, false
		}
		return JWK{
			Kty: "RSA",
			Kid: k.ID,
			Alg: string(AlgRS256),
			Use: "sig",
			N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E:   encodeRSAExponent(pub.E),
		}, true
	case AlgES256, AlgES384:
		pub, ok := k.Public.(*ecdsa.PublicKey)
		if !ok {
			return JWK{}, false
		}
		// pub.Bytes returns the SEC1 uncompressed form: 0x04 || X || Y
		// with each coordinate already fixed-width. Strip the 0x04 prefix
		// and split — avoids touching the deprecated big.Int accessors.
		raw, err := pub.Bytes()
		if err != nil || len(raw) < 1 || raw[0] != 0x04 {
			return JWK{}, false
		}
		coords := raw[1:]
		if len(coords)%2 != 0 {
			return JWK{}, false
		}
		coordSize := len(coords) / 2
		crv := "P-256"
		if k.Alg == AlgES384 {
			crv = "P-384"
		}
		return JWK{
			Kty: "EC",
			Kid: k.ID,
			Alg: string(k.Alg),
			Use: "sig",
			Crv: crv,
			X:   base64.RawURLEncoding.EncodeToString(coords[:coordSize]),
			Y:   base64.RawURLEncoding.EncodeToString(coords[coordSize:]),
		}, true
	default:
		return JWK{}, false
	}
}


// encodeRSAExponent renders an RSA public exponent in JWS-canonical
// base64url form (RFC 7518 §6.3.1.2): big-endian, minimum-length
// byte string. Almost always 65537 → "AQAB", but small primes are
// handled too.
func encodeRSAExponent(e int) string {
	var buf []byte
	for e > 0 {
		buf = append([]byte{byte(e & 0xff)}, buf...)
		e >>= 8
	}
	if len(buf) == 0 {
		buf = []byte{0}
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
