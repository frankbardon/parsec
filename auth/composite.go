package auth

import (
	"context"
	"errors"
)

// CompositeVerifier tries the parsec HMAC verifier first; if that
// fails AND an OIDC verifier is configured, it falls back to OIDC. A
// success from either path wins. When both fail, the HMAC error is
// returned (the HMAC path is the primary verification surface — its
// error code maps cleanly onto PARSEC_AUTH_*).
//
// A nil OIDC field reduces this to a pass-through over HMAC, so
// deployments without OIDCConfig accept only HMAC-signed tokens.
type CompositeVerifier struct {
	// HMAC is the parsec-issued JWT verifier. Required.
	HMAC *Verifier

	// OIDC validates ID tokens from a configured IdP. Optional —
	// when nil the composite is HMAC-only.
	OIDC *OIDCVerifier
}

// NewCompositeVerifier returns a verifier that composes an HMAC
// verifier with an optional OIDC verifier. hmac must be non-nil
// (parsec.New always constructs one).
func NewCompositeVerifier(hmac *Verifier, oidc *OIDCVerifier) (*CompositeVerifier, error) {
	if hmac == nil {
		return nil, errors.New("auth: CompositeVerifier requires an HMAC verifier")
	}
	return &CompositeVerifier{HMAC: hmac, OIDC: oidc}, nil
}

// Verify tries the HMAC verifier first; on any failure, and if OIDC
// is wired, it tries the OIDC verifier. The first success wins. When
// both fail, the HMAC error is returned because it is the more
// specific path (operator-minted tokens dominate inbound traffic on a
// typical deployment).
//
// Verify is safe for concurrent use. The expected Type is enforced
// against the HMAC payload; OIDC tokens always synthesize Typ=TypeMgmt
// and so are only honored when expected is empty or TypeMgmt.
func (c *CompositeVerifier) Verify(ctx context.Context, token string, expected Type) (Claims, error) {
	if c.HMAC != nil {
		claims, err := c.HMAC.Verify(token, expected)
		if err == nil {
			return claims, nil
		}
		// HMAC failed. If OIDC is enabled AND the caller expects a
		// mgmt token (or no specific type), try OIDC. For access /
		// refresh tokens, OIDC has nothing useful to contribute.
		if c.OIDC == nil || (expected != "" && expected != TypeMgmt) {
			return claims, err
		}
		oidcClaims, oidcErr := c.OIDC.Verify(ctx, token)
		if oidcErr == nil {
			return oidcClaims, nil
		}
		// Both failed. Surface the HMAC error: the HMAC path is the
		// canonical verifier and its sentinels map directly onto
		// PARSEC_AUTH_* codes.
		return claims, err
	}
	// HMAC is required; this branch is unreachable from
	// NewCompositeVerifier but kept defensive.
	if c.OIDC != nil {
		return c.OIDC.Verify(ctx, token)
	}
	return Claims{}, ErrMalformedToken
}

// HMACVerify validates token strictly via the HMAC path. Useful for
// surface code that must reject OIDC-shaped tokens (e.g. refresh-token
// RPC, which has no OIDC analog).
func (c *CompositeVerifier) HMACVerify(token string, expected Type) (Claims, error) {
	return c.HMAC.Verify(token, expected)
}

// OIDCEnabled reports whether an OIDC verifier is wired.
func (c *CompositeVerifier) OIDCEnabled() bool { return c != nil && c.OIDC != nil }
