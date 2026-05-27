package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testRing(t *testing.T) *KeyRing {
	t.Helper()
	r := NewKeyRing()
	if _, err := r.Generate(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSigner_RoundTrip(t *testing.T) {
	ring := testRing(t)
	signer, err := NewSigner(ring)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(ring)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	in := Claims{
		Sub: "user-42", Typ: TypeAccess, Chs: []string{"public:test.x.y"},
		Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(),
	}
	tok, err := signer.Sign(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(tok, ".") != 2 {
		t.Fatalf("expected compact JWT form: %s", tok)
	}
	out, err := verifier.Verify(tok, TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if out.Sub != in.Sub || out.Typ != in.Typ || out.Exp != in.Exp {
		t.Fatalf("claim mismatch: in=%+v out=%+v", in, out)
	}
}

func TestSigner_RequiresActiveKey(t *testing.T) {
	signer, err := NewSigner(NewKeyRing())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, err = signer.Sign(Claims{Typ: TypeAccess, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()})
	if err == nil {
		t.Fatal("expected sign to fail on empty ring")
	}
}

func TestVerifier_RejectsExpired(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	verifier, _ := NewVerifier(ring)
	long := time.Now().Add(-2 * time.Hour)
	tok, _ := signer.Sign(Claims{
		Typ: TypeAccess, Iat: long.Unix(), Exp: long.Add(time.Hour).Unix(),
	})
	_, err := verifier.Verify(tok, TypeAccess)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

func TestVerifier_RejectsTypeMismatch(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	verifier, _ := NewVerifier(ring)
	now := time.Now()
	tok, _ := signer.Sign(Claims{
		Typ: TypeRefresh, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(),
	})
	_, err := verifier.Verify(tok, TypeAccess)
	if !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected ErrTypeMismatch, got %v", err)
	}
}

func TestVerifier_RejectsTokenSignedByOtherRing(t *testing.T) {
	signerRing := testRing(t)
	verifyRing := testRing(t)
	signer, _ := NewSigner(signerRing)
	verifier, _ := NewVerifier(verifyRing)
	now := time.Now()
	tok, _ := signer.Sign(Claims{
		Typ: TypeAccess, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(),
	})
	_, err := verifier.Verify(tok, TypeAccess)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestVerifier_RejectsTokenWithoutKid(t *testing.T) {
	ring := testRing(t)
	verifier, _ := NewVerifier(ring)
	// Hand-craft a token with the legacy kid-less header.
	legacy := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ0eXAiOiJhY2Nlc3MiLCJpYXQiOjAsImV4cCI6MX0.AAAA"
	_, err := verifier.Verify(legacy, TypeAccess)
	if !errors.Is(err, ErrMalformedToken) {
		t.Fatalf("expected ErrMalformedToken for kid-less header, got %v", err)
	}
}

func TestVerifier_RejectsUnknownKid(t *testing.T) {
	signerRing := testRing(t)
	signer, _ := NewSigner(signerRing)
	now := time.Now()
	tok, _ := signer.Sign(Claims{
		Typ: TypeAccess, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(),
	})
	// Fresh ring with a different kid.
	otherRing := testRing(t)
	verifier, _ := NewVerifier(otherRing)
	_, err := verifier.Verify(tok, TypeAccess)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for unknown kid, got %v", err)
	}
}

func TestVerifier_RejectsTamperedAlg(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	verifier, _ := NewVerifier(ring)
	now := time.Now()
	tok, _ := signer.Sign(Claims{
		Typ: TypeAccess, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix(),
	})
	parts := strings.Split(tok, ".")
	// Replace the header with one that claims alg=none and a real kid.
	id, _ := ring.Active()
	parts[0] = encodeJSON(`{"alg":"none","kid":"` + id.ID + `","typ":"JWT"}`)
	_, err := verifier.Verify(strings.Join(parts, "."), TypeAccess)
	if !errors.Is(err, ErrUnsupportedAlg) {
		t.Fatalf("expected ErrUnsupportedAlg, got %v", err)
	}
}

func TestVerifier_RejectsMalformed(t *testing.T) {
	ring := testRing(t)
	verifier, _ := NewVerifier(ring)
	for _, s := range []string{"", "abc", "a.b", "a.b.c.d"} {
		if _, err := verifier.Verify(s, TypeAccess); !errors.Is(err, ErrMalformedToken) {
			t.Errorf("expected ErrMalformedToken for %q, got %v", s, err)
		}
	}
}

// encodeJSON returns the base64url-without-padding encoding of s. Test
// helper used to hand-craft tampered tokens.
func encodeJSON(s string) string {
	return rawURLEncode([]byte(s))
}
