package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
)

// mockIdP is a tiny OIDC issuer used by the verifier tests. It signs
// ID tokens with an RSA key and serves the JWKS document so go-oidc's
// Verifier can fetch the public key during discovery.
type mockIdP struct {
	srv     *httptest.Server
	priv    *rsa.PrivateKey
	kid     string
	issuer  string
	mux     *http.ServeMux
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &mockIdP{priv: priv, kid: "test-kid"}
	idp.mux = http.NewServeMux()
	idp.srv = httptest.NewServer(idp.mux)
	idp.issuer = idp.srv.URL

	idp.mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.issuer,
			"jwks_uri":                              idp.issuer + "/jwks",
			"authorization_endpoint":                idp.issuer + "/authz",
			"token_endpoint":                        idp.issuer + "/token",
			"device_authorization_endpoint":         idp.issuer + "/device",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	idp.mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(priv.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kid": idp.kid,
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"n":   n,
				"e":   e,
			}},
		})
	})
	return idp
}

func (idp *mockIdP) close() { idp.srv.Close() }

// signToken builds an RS256-signed JWT with the given claims.
func (idp *mockIdP) signToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "kid": idp.kid, "typ": "JWT"}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(claims)
	h := base64.RawURLEncoding.EncodeToString(hb)
	p := base64.RawURLEncoding.EncodeToString(pb)
	signing := h + "." + p
	hash := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.priv, 5, hash[:]) // 5 = crypto.SHA256
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func baseClaims(idp *mockIdP, aud string, ttl time.Duration) map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"iss":   idp.issuer,
		"aud":   aud,
		"sub":   "user-123",
		"email": "alice@example.com",
		"iat":   now,
		"exp":   time.Now().Add(ttl).Unix(),
	}
}

func TestOIDCVerifier_AcceptsValidToken(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()

	cfg := auth.OIDCConfig{
		Issuer:       idp.issuer,
		Audience:     "parsec-test",
		SubjectClaim: "email",
		ScopesClaim:  "groups",
	}
	v, err := auth.NewOIDCVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	claims := baseClaims(idp, "parsec-test", time.Hour)
	claims["groups"] = []string{"parsec-admins"}
	tok := idp.signToken(t, claims)

	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if c.Typ != auth.TypeMgmt {
		t.Errorf("typ = %q, want mgmt", c.Typ)
	}
	if c.Sub != "alice@example.com" {
		t.Errorf("sub = %q, want alice@example.com (from email claim)", c.Sub)
	}
}

func TestOIDCVerifier_GroupToScopeMapping(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()

	cfg := auth.OIDCConfig{
		Issuer:   idp.issuer,
		Audience: "parsec-test",
		Grants: []auth.OIDCGrant{
			{IfGroup: "parsec-admins", Scope: "*:**", Verbs: []auth.Verb{auth.VerbSubscribe, auth.VerbPublish, auth.VerbManage}},
			{IfGroup: "parsec-readers", Scope: "public:**", Verbs: []auth.Verb{auth.VerbSubscribe}},
		},
	}
	v, err := auth.NewOIDCVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	claims := baseClaims(idp, "parsec-test", time.Hour)
	claims["groups"] = []string{"parsec-admins", "unrelated-group"}
	tok := idp.signToken(t, claims)
	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(c.Scopes) != 1 {
		t.Fatalf("scopes len = %d, want 1; got %+v", len(c.Scopes), c.Scopes)
	}
	if c.Scopes[0].Pattern != "*:**" {
		t.Errorf("scope pattern = %q, want *:**", c.Scopes[0].Pattern)
	}
	if len(c.Scopes[0].Verbs) != 3 {
		t.Errorf("verbs len = %d, want 3", len(c.Scopes[0].Verbs))
	}

	// Reader only.
	claims2 := baseClaims(idp, "parsec-test", time.Hour)
	claims2["groups"] = []string{"parsec-readers"}
	tok2 := idp.signToken(t, claims2)
	c2, err := v.Verify(context.Background(), tok2)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.Scopes) != 1 || c2.Scopes[0].Pattern != "public:**" {
		t.Errorf("reader scopes = %+v, want public:**", c2.Scopes)
	}
}

func TestOIDCVerifier_RejectsExpiredToken(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	cfg := auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-test"}
	v, err := auth.NewOIDCVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	claims := baseClaims(idp, "parsec-test", -time.Hour) // already expired
	tok := idp.signToken(t, claims)
	_, err = v.Verify(context.Background(), tok)
	if err == nil {
		t.Fatal("expected expired-token error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "expired") {
		t.Errorf("error = %v, want expired-token sentinel", err)
	}
}

func TestOIDCVerifier_RejectsBadAudience(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	cfg := auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-test"}
	v, err := auth.NewOIDCVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	claims := baseClaims(idp, "wrong-audience", time.Hour)
	tok := idp.signToken(t, claims)
	_, err = v.Verify(context.Background(), tok)
	if err == nil {
		t.Fatal("expected audience-mismatch error, got nil")
	}
}

func TestOIDCVerifier_RejectsBadSignature(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	cfg := auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-test"}
	v, err := auth.NewOIDCVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	claims := baseClaims(idp, "parsec-test", time.Hour)
	tok := idp.signToken(t, claims)
	// Tamper with the signature.
	parts := strings.Split(tok, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString([]byte("tamper"))
	tampered := strings.Join(parts, ".")
	_, err = v.Verify(context.Background(), tampered)
	if err == nil {
		t.Fatal("expected signature-failure error, got nil")
	}
}

func TestOIDCVerifier_RequiresIssuer(t *testing.T) {
	_, err := auth.NewOIDCVerifier(context.Background(), auth.OIDCConfig{Audience: "x"})
	if err == nil {
		t.Fatal("expected error when issuer is empty")
	}
}

func TestOIDCVerifier_RequiresAudience(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	_, err := auth.NewOIDCVerifier(context.Background(), auth.OIDCConfig{Issuer: idp.issuer})
	if err == nil {
		t.Fatal("expected error when audience is empty")
	}
}

func TestOIDCVerifier_NoGroupsNoScopes(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	cfg := auth.OIDCConfig{
		Issuer:   idp.issuer,
		Audience: "parsec-test",
		Grants:   []auth.OIDCGrant{{IfGroup: "admins", Scope: "*:**", Verbs: []auth.Verb{auth.VerbManage}}},
	}
	v, err := auth.NewOIDCVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	claims := baseClaims(idp, "parsec-test", time.Hour) // no groups claim
	tok := idp.signToken(t, claims)
	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Scopes) != 0 {
		t.Errorf("expected empty scopes, got %+v", c.Scopes)
	}
}

// Sanity check that the mock IdP is actually serving discovery —
// guards against accidental harness drift.
func TestMockIdP_ServesDiscovery(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	resp, err := http.Get(idp.issuer + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	if doc["issuer"] != idp.issuer {
		t.Errorf("issuer mismatch: %s vs %s", doc["issuer"], idp.issuer)
	}
	_ = fmt.Sprintf("%v", doc) // touch
}
