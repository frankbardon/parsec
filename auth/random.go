package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// GenerateSecret returns a 32-byte cryptographically random secret suitable
// for Signer / Verifier. The caller persists it (env var, secrets store);
// regenerating wipes every previously-issued token.
func GenerateSecret() ([]byte, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, errors.New("auth: secret generation failed")
	}
	return buf, nil
}

// newTokenID returns a 128-bit URL-safe random identifier used as JTI
// and FID claims on refresh tokens. The encoding is base64url without
// padding so the value drops into a JWT claim without quoting issues.
func newTokenID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", errors.New("auth: token id generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
