package auth

import (
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newClaims() Claims {
	now := time.Now().Unix()
	return Claims{
		Sub: "alice",
		Typ: TypeAccess,
		Chs: []string{"public:web.notify.demo"},
		Iat: now,
		Exp: now + 60,
	}
}

func TestSignVerifyEd25519(t *testing.T) {
	r := NewKeyRing()
	k, err := r.GenerateEd25519()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if k.Alg != AlgEdDSA {
		t.Fatalf("alg = %s", k.Alg)
	}
	if _, ok := k.Public.(ed25519.PublicKey); !ok {
		t.Fatalf("public type = %T", k.Public)
	}
	signer, _ := NewSigner(r)
	v, _ := NewVerifier(r)
	tok, err := signer.Sign(newClaims())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := v.Verify(tok, TypeAccess)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Sub != "alice" {
		t.Fatalf("sub = %q", got.Sub)
	}
}

func TestSignVerifyRS256(t *testing.T) {
	r := NewKeyRing()
	k, err := r.GenerateRSA(2048)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if k.Alg != AlgRS256 {
		t.Fatalf("alg = %s", k.Alg)
	}
	if _, ok := k.Public.(*rsa.PublicKey); !ok {
		t.Fatalf("public type = %T", k.Public)
	}
	signer, _ := NewSigner(r)
	v, _ := NewVerifier(r)
	tok, err := signer.Sign(newClaims())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := v.Verify(tok, TypeAccess); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestGenerateRSARejectsSmallKey(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.GenerateRSA(1024); err == nil {
		t.Fatal("expected rejection of 1024-bit RSA key")
	}
}

func TestMixedRingRotation(t *testing.T) {
	r := NewKeyRing()
	hs, err := r.Generate()
	if err != nil {
		t.Fatal(err)
	}
	ed, err := r.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := NewSigner(r)
	v, _ := NewVerifier(r)

	tok1, err := signer.Sign(newClaims())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok1, mustHeader(t, hs.ID, AlgHS256)+".") {
		t.Fatal("token1 not signed by HS256 key")
	}

	if err := r.Promote(ed.ID); err != nil {
		t.Fatal(err)
	}
	tok2, err := signer.Sign(newClaims())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok2, mustHeader(t, ed.ID, AlgEdDSA)+".") {
		t.Fatal("token2 not signed by Ed25519 key")
	}

	if _, err := v.Verify(tok1, TypeAccess); err != nil {
		t.Fatalf("verify HS token after rotation: %v", err)
	}
	if _, err := v.Verify(tok2, TypeAccess); err != nil {
		t.Fatalf("verify Ed token: %v", err)
	}
}

// mustHeader returns the base64url-encoded JOSE header for kid+alg.
func mustHeader(t *testing.T, kid string, alg Alg) string {
	t.Helper()
	h, err := buildHeader(kid, alg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestVerifyRejectsAlgMismatch(t *testing.T) {
	// Mint a token under an Ed25519 key, then forge a header that
	// claims HS256 for the same kid. The verifier MUST reject — this
	// is the canonical key-confusion attack.
	r := NewKeyRing()
	ed, err := r.GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := NewSigner(r)
	v, _ := NewVerifier(r)
	tok, err := signer.Sign(newClaims())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token shape: %s", tok)
	}
	forgedHdr, err := buildHeader(ed.ID, AlgHS256)
	if err != nil {
		t.Fatal(err)
	}
	forged := forgedHdr + "." + parts[1] + "." + parts[2]
	if _, err := v.Verify(forged, TypeAccess); err == nil {
		t.Fatal("verifier accepted alg-swapped header")
	}
}

func TestSnapshotRoundTripAsymmetric(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.GenerateEd25519(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GenerateRSA(2048); err != nil {
		t.Fatal(err)
	}
	snap := r.Snapshot()
	r2 := NewKeyRing()
	if err := r2.LoadSnapshot(snap); err != nil {
		t.Fatalf("load: %v", err)
	}
	if r2.ActiveID() != r.ActiveID() {
		t.Fatalf("active id drift")
	}
	for _, original := range r.List() {
		got, err := r2.Get(original.ID)
		if err != nil {
			t.Fatalf("get %s: %v", original.ID, err)
		}
		if got.Alg != original.Alg {
			t.Fatalf("alg drift: %s != %s", got.Alg, original.Alg)
		}
	}
	// Sign/verify still works through the reloaded ring.
	s2, _ := NewSigner(r2)
	v2, _ := NewVerifier(r2)
	tok, err := s2.Sign(newClaims())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.Verify(tok, TypeAccess); err != nil {
		t.Fatalf("verify after reload: %v", err)
	}
}

func TestSnapshotLegacyV1Loads(t *testing.T) {
	// A v1-shaped entry has no Alg + no PrivatePEM; SecretHex carries
	// the HMAC secret. The loader treats this as HS256.
	r := NewKeyRing()
	k, _ := r.Generate()
	snap := r.Snapshot()
	// Strip Alg from each entry to simulate the legacy shape.
	for i := range snap.Keys {
		snap.Keys[i].Alg = ""
	}
	r2 := NewKeyRing()
	if err := r2.LoadSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	got, err := r2.Get(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Alg != AlgHS256 {
		t.Fatalf("legacy alg = %s, want HS256", got.Alg)
	}
}

func TestJWKSHandler(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.GenerateEd25519(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GenerateRSA(2048); err != nil {
		t.Fatal(err)
	}
	// Mix in an HMAC key — JWKS must NOT include it.
	hsRing := NewKeyRing()
	hs, _ := hsRing.Generate()
	r.byID[hs.ID] = hsRing.byID[hs.ID]

	srv := httptest.NewServer(JWKSHandler(r))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "application/jwk-set+json" {
		t.Fatalf("content-type = %q", got)
	}
	var jwks JWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		t.Fatal(err)
	}
	if len(jwks.Keys) != 2 {
		t.Fatalf("got %d keys, want 2 (HMAC excluded)", len(jwks.Keys))
	}
	for _, jwk := range jwks.Keys {
		switch jwk.Kty {
		case "OKP":
			if jwk.Crv != "Ed25519" {
				t.Fatalf("OKP crv = %q", jwk.Crv)
			}
			if _, err := base64.RawURLEncoding.DecodeString(jwk.X); err != nil {
				t.Fatalf("OKP x decode: %v", err)
			}
		case "RSA":
			if _, err := base64.RawURLEncoding.DecodeString(jwk.N); err != nil {
				t.Fatalf("RSA n decode: %v", err)
			}
			// e=65537 → "AQAB"
			if jwk.E != "AQAB" {
				t.Fatalf("RSA e = %q, want AQAB", jwk.E)
			}
		default:
			t.Fatalf("unexpected kty %q", jwk.Kty)
		}
	}
}

func TestJWKSExcludesRetired(t *testing.T) {
	r := NewKeyRing()
	hs, _ := r.Generate()
	ed, _ := r.GenerateEd25519()
	if err := r.Promote(ed.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(hs.ID); err != nil {
		t.Fatal(err)
	}
	out := jwksFromRing(r)
	if len(out.Keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(out.Keys))
	}
	if out.Keys[0].Kid != ed.ID {
		t.Fatalf("kid = %s, want %s", out.Keys[0].Kid, ed.ID)
	}
}
