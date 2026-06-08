package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSignVerifyES256 mints + verifies a P-256 ECDSA token end-to-end.
// The JOSE header carries alg=ES256 and the signature is the
// fixed-width r||s encoding (64 bytes for P-256).
func TestSignVerifyES256(t *testing.T) {
	r := NewKeyRing()
	k, err := r.GenerateECDSA(elliptic.P256())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if k.Alg != AlgES256 {
		t.Fatalf("alg = %s, want ES256", k.Alg)
	}
	pub, ok := k.Public.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("public type = %T", k.Public)
	}
	if pub.Curve != elliptic.P256() {
		t.Fatalf("curve = %s, want P-256", pub.Curve.Params().Name)
	}
	signer, _ := NewSigner(r)
	v, _ := NewVerifier(r)
	tok, err := signer.Sign(newClaims())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token shape: %s", tok)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("ES256 sig len = %d, want 64", len(sig))
	}
	got, err := v.Verify(tok, TypeAccess)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Sub != "alice" {
		t.Fatalf("sub = %q", got.Sub)
	}
}

// TestSignVerifyES384 mints + verifies a P-384 ECDSA token end-to-end.
// The signature width is 96 bytes (r||s with each coordinate padded
// to 48 bytes).
func TestSignVerifyES384(t *testing.T) {
	r := NewKeyRing()
	k, err := r.GenerateECDSA(elliptic.P384())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if k.Alg != AlgES384 {
		t.Fatalf("alg = %s, want ES384", k.Alg)
	}
	signer, _ := NewSigner(r)
	v, _ := NewVerifier(r)
	tok, err := signer.Sign(newClaims())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts := strings.Split(tok, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 96 {
		t.Fatalf("ES384 sig len = %d, want 96", len(sig))
	}
	if _, err := v.Verify(tok, TypeAccess); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// TestGenerateECDSARejectsUnsupportedCurve refuses P-224 and P-521 —
// the supported set is exactly P-256 and P-384 since JOSE pins the
// alg→curve mapping.
func TestGenerateECDSARejectsUnsupportedCurve(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.GenerateECDSA(elliptic.P224()); err == nil {
		t.Fatal("expected rejection of P-224")
	}
	if _, err := r.GenerateECDSA(elliptic.P521()); err == nil {
		t.Fatal("expected rejection of P-521")
	}
}

// TestECDSAGenerateAlgRouting confirms GenerateAlg routes ES256/ES384
// to the right curve.
func TestECDSAGenerateAlgRouting(t *testing.T) {
	for _, tc := range []struct {
		alg   Alg
		curve elliptic.Curve
	}{
		{AlgES256, elliptic.P256()},
		{AlgES384, elliptic.P384()},
	} {
		r := NewKeyRing()
		k, err := r.GenerateAlg(tc.alg)
		if err != nil {
			t.Fatalf("%s: generate: %v", tc.alg, err)
		}
		pub := k.Public.(*ecdsa.PublicKey)
		if pub.Curve != tc.curve {
			t.Fatalf("%s: curve = %s, want %s", tc.alg, pub.Curve.Params().Name, tc.curve.Params().Name)
		}
	}
}

// TestECDSAVerifyRejectsAlgMismatch is the key-confusion attack guard:
// a token signed with an ES256 key is presented with a header that
// claims ES384 for the same kid. The verifier must reject — even
// before reaching ecdsa.Verify, because the alg-vs-key check fires.
func TestECDSAVerifyRejectsAlgMismatch(t *testing.T) {
	r := NewKeyRing()
	k, err := r.GenerateECDSA(elliptic.P256())
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
	forgedHdr, err := buildHeader(k.ID, AlgES384)
	if err != nil {
		t.Fatal(err)
	}
	forged := forgedHdr + "." + parts[1] + "." + parts[2]
	if _, err := v.Verify(forged, TypeAccess); err == nil {
		t.Fatal("verifier accepted ES256→ES384 alg swap")
	}
}

// TestECDSAVerifyRejectsTruncatedSignature confirms a JOSE sig that
// isn't the exact 2*coordSize width is rejected (defends against
// trailing-byte tampering and length-coercion).
func TestECDSAVerifyRejectsTruncatedSignature(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.GenerateECDSA(elliptic.P256()); err != nil {
		t.Fatal(err)
	}
	signer, _ := NewSigner(r)
	v, _ := NewVerifier(r)
	tok, err := signer.Sign(newClaims())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	truncated := base64.RawURLEncoding.EncodeToString(sig[:len(sig)-1])
	bad := parts[0] + "." + parts[1] + "." + truncated
	if _, err := v.Verify(bad, TypeAccess); err == nil {
		t.Fatal("verifier accepted truncated ECDSA signature")
	}
}

// TestECDSASnapshotRoundTrip exercises the PKCS#8 marshalling
// round-trip for ECDSA keys: keys reload at the same kid, the active
// kid is preserved, and a fresh sign/verify still succeeds.
func TestECDSASnapshotRoundTrip(t *testing.T) {
	r := NewKeyRing()
	es256, err := r.GenerateECDSA(elliptic.P256())
	if err != nil {
		t.Fatal(err)
	}
	es384, err := r.GenerateECDSA(elliptic.P384())
	if err != nil {
		t.Fatal(err)
	}

	snap := r.Snapshot()
	r2 := NewKeyRing()
	if err := r2.LoadSnapshot(snap); err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, want := range []Key{es256, es384} {
		got, err := r2.Get(want.ID)
		if err != nil {
			t.Fatalf("get %s: %v", want.ID, err)
		}
		if got.Alg != want.Alg {
			t.Fatalf("alg drift: %s != %s", got.Alg, want.Alg)
		}
		gp := got.Public.(*ecdsa.PublicKey)
		wp := want.Public.(*ecdsa.PublicKey)
		if gp.Curve != wp.Curve {
			t.Fatalf("curve drift: %s != %s", gp.Curve.Params().Name, wp.Curve.Params().Name)
		}
	}
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

// TestJWKS_ECDSA_ExposesXY confirms ES256 + ES384 keys land in JWKS
// with kty=EC, the correct crv label, and base64url-encoded fixed-
// width x/y coordinates.
func TestJWKS_ECDSA_ExposesXY(t *testing.T) {
	r := NewKeyRing()
	if _, err := r.GenerateECDSA(elliptic.P256()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GenerateECDSA(elliptic.P384()); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(JWKSHandler(r))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var jwks JWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		t.Fatal(err)
	}
	if len(jwks.Keys) != 2 {
		t.Fatalf("want 2 EC keys, got %d", len(jwks.Keys))
	}
	wantSizes := map[string]int{"P-256": 32, "P-384": 48}
	for _, jwk := range jwks.Keys {
		if jwk.Kty != "EC" {
			t.Fatalf("kty = %q, want EC", jwk.Kty)
		}
		size, ok := wantSizes[jwk.Crv]
		if !ok {
			t.Fatalf("unexpected crv %q", jwk.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(jwk.X)
		if err != nil || len(x) != size {
			t.Fatalf("crv=%s x len=%d err=%v", jwk.Crv, len(x), err)
		}
		y, err := base64.RawURLEncoding.DecodeString(jwk.Y)
		if err != nil || len(y) != size {
			t.Fatalf("crv=%s y len=%d err=%v", jwk.Crv, len(y), err)
		}
	}
}

// TestECDSAMixedRingRotation rotates HS256 → ES256 → ES384 and
// confirms each rotation step minted under the right kid, and that
// every prior token still verifies against the verify-only keys.
func TestECDSAMixedRingRotation(t *testing.T) {
	r := NewKeyRing()
	hs, err := r.Generate()
	if err != nil {
		t.Fatal(err)
	}
	es256, err := r.GenerateECDSA(elliptic.P256())
	if err != nil {
		t.Fatal(err)
	}
	es384, err := r.GenerateECDSA(elliptic.P384())
	if err != nil {
		t.Fatal(err)
	}

	signer, _ := NewSigner(r)
	v, _ := NewVerifier(r)

	tokHS, _ := signer.Sign(newClaims())
	if !strings.HasPrefix(tokHS, mustHeader(t, hs.ID, AlgHS256)+".") {
		t.Fatal("HS token wrong kid")
	}
	if err := r.Promote(es256.ID); err != nil {
		t.Fatal(err)
	}
	tokES256, _ := signer.Sign(newClaims())
	if !strings.HasPrefix(tokES256, mustHeader(t, es256.ID, AlgES256)+".") {
		t.Fatal("ES256 token wrong kid")
	}
	if err := r.Promote(es384.ID); err != nil {
		t.Fatal(err)
	}
	tokES384, _ := signer.Sign(newClaims())
	if !strings.HasPrefix(tokES384, mustHeader(t, es384.ID, AlgES384)+".") {
		t.Fatal("ES384 token wrong kid")
	}

	for name, tok := range map[string]string{"HS": tokHS, "ES256": tokES256, "ES384": tokES384} {
		if _, err := v.Verify(tok, TypeAccess); err != nil {
			t.Fatalf("%s verify: %v", name, err)
		}
	}
}

// TestSupportedAlgsIncludesECDSA confirms the manifest-facing list
// surfaces the new algs in operator-preferred order.
func TestSupportedAlgsIncludesECDSA(t *testing.T) {
	got := SupportedAlgs()
	want := []Alg{AlgHS256, AlgEdDSA, AlgES256, AlgES384, AlgRS256}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, a := range want {
		if got[i] != a {
			t.Fatalf("[%d] = %s, want %s", i, got[i], a)
		}
	}
}
