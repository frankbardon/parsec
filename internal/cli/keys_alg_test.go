package cli

import "testing"

// TestNormalizeAlg confirms every operator-facing alg string the CLI
// accepts maps to the canonical wire form the service layer expects.
// Includes the new ECDSA aliases (es256 / es384).
func TestNormalizeAlg(t *testing.T) {
	cases := map[string]string{
		"":         "HS256",
		"hs256":    "HS256",
		"HS256":    "HS256",
		"rs256":    "RS256",
		"eddsa":    "EdDSA",
		"ed25519":  "EdDSA",
		"es256":    "ES256",
		"ES256":    "ES256",
		"es384":    "ES384",
		"unknown":  "unknown",
	}
	for in, want := range cases {
		if got := normalizeAlg(in); got != want {
			t.Errorf("normalizeAlg(%q) = %q, want %q", in, got, want)
		}
	}
}
