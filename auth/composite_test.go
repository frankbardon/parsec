package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
)

// mintHMACMgmtToken returns an HMAC-signed mgmt token + the verifier
// that accepts it. Used so the composite tests can exercise both
// success paths without standing up a full parsec instance.
func mintHMACMgmtToken(t *testing.T, sub string, ttl time.Duration) (string, *auth.Verifier) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := auth.NewKeyRingFromSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(ring)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewVerifier(ring)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok, err := signer.Sign(auth.Claims{
		Sub: sub,
		Typ: auth.TypeMgmt,
		Iat: now.Unix(),
		Exp: now.Add(ttl).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok, verifier
}

func TestComposite_HMACSuccess(t *testing.T) {
	tok, ver := mintHMACMgmtToken(t, "operator", time.Hour)
	idp := newMockIdP(t)
	defer idp.close()
	ov, err := auth.NewOIDCVerifier(context.Background(), auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-test"})
	if err != nil {
		t.Fatal(err)
	}
	cv, err := auth.NewCompositeVerifier(ver, ov)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cv.Verify(context.Background(), tok, auth.TypeMgmt)
	if err != nil {
		t.Fatalf("expected HMAC accept, got %v", err)
	}
	if c.Sub != "operator" {
		t.Errorf("sub = %q, want operator", c.Sub)
	}
}

func TestComposite_OIDCFallback(t *testing.T) {
	_, ver := mintHMACMgmtToken(t, "operator", time.Hour)
	idp := newMockIdP(t)
	defer idp.close()
	ov, err := auth.NewOIDCVerifier(context.Background(), auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-test"})
	if err != nil {
		t.Fatal(err)
	}
	cv, err := auth.NewCompositeVerifier(ver, ov)
	if err != nil {
		t.Fatal(err)
	}
	claims := baseClaims(idp, "parsec-test", time.Hour)
	tok := idp.signToken(t, claims)
	c, err := cv.Verify(context.Background(), tok, auth.TypeMgmt)
	if err != nil {
		t.Fatalf("expected OIDC fallback to succeed, got %v", err)
	}
	if c.Typ != auth.TypeMgmt {
		t.Errorf("typ = %q, want mgmt", c.Typ)
	}
}

func TestComposite_BogusTokenRejected(t *testing.T) {
	_, ver := mintHMACMgmtToken(t, "operator", time.Hour)
	idp := newMockIdP(t)
	defer idp.close()
	ov, err := auth.NewOIDCVerifier(context.Background(), auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-test"})
	if err != nil {
		t.Fatal(err)
	}
	cv, err := auth.NewCompositeVerifier(ver, ov)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cv.Verify(context.Background(), "not-a-jwt", auth.TypeMgmt)
	if err == nil {
		t.Fatal("expected rejection")
	}
}

func TestComposite_NoOIDCIsHMACOnly(t *testing.T) {
	tok, ver := mintHMACMgmtToken(t, "operator", time.Hour)
	cv, err := auth.NewCompositeVerifier(ver, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cv.Verify(context.Background(), tok, auth.TypeMgmt)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sub != "operator" {
		t.Errorf("sub = %q, want operator", c.Sub)
	}
	// OIDC-shaped token must be rejected when OIDC is not wired.
	_, err = cv.Verify(context.Background(), "some.fake.oidc.token", auth.TypeMgmt)
	if err == nil {
		t.Fatal("expected rejection in HMAC-only mode")
	}
}

func TestComposite_HMACFailureFallsThroughToOIDC(t *testing.T) {
	// Build two distinct HMAC keyrings so the operator's token does
	// not validate against the composite's verifier — forces the
	// composite to fall through to OIDC.
	_, ver := mintHMACMgmtToken(t, "operator", time.Hour)
	otherTok, _ := mintHMACMgmtToken(t, "intruder", time.Hour) // different secret
	idp := newMockIdP(t)
	defer idp.close()
	ov, err := auth.NewOIDCVerifier(context.Background(), auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-test"})
	if err != nil {
		t.Fatal(err)
	}
	cv, err := auth.NewCompositeVerifier(ver, ov)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: the other token should not pass HMAC nor OIDC — both
	// fail, so we expect an HMAC error.
	_, err = cv.Verify(context.Background(), otherTok, auth.TypeMgmt)
	if err == nil {
		t.Fatal("expected both verifiers to reject the intruder token")
	}
	// Now mint a valid OIDC token — HMAC must fail (malformed), and
	// OIDC must accept.
	claims := baseClaims(idp, "parsec-test", time.Hour)
	tok := idp.signToken(t, claims)
	if _, err := cv.Verify(context.Background(), tok, auth.TypeMgmt); err != nil {
		t.Fatalf("OIDC fallback failed: %v", err)
	}
}

func TestComposite_NilHMACErrors(t *testing.T) {
	_, err := auth.NewCompositeVerifier(nil, nil)
	if err == nil {
		t.Fatal("expected error when HMAC is nil")
	}
	if !errors.Is(err, err) { // smoke: error round-trips
		t.Errorf("error: %v", err)
	}
}

func TestComposite_OIDCEnabled(t *testing.T) {
	_, ver := mintHMACMgmtToken(t, "operator", time.Hour)
	cv, _ := auth.NewCompositeVerifier(ver, nil)
	if cv.OIDCEnabled() {
		t.Errorf("OIDCEnabled = true on HMAC-only composite")
	}
	idp := newMockIdP(t)
	defer idp.close()
	ov, _ := auth.NewOIDCVerifier(context.Background(), auth.OIDCConfig{Issuer: idp.issuer, Audience: "x"})
	cv2, _ := auth.NewCompositeVerifier(ver, ov)
	if !cv2.OIDCEnabled() {
		t.Errorf("OIDCEnabled = false when OIDC is wired")
	}
}
