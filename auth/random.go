package auth

import (
	"crypto/rand"
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
