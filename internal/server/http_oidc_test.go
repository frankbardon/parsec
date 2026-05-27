package server_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/internal/server"
	"github.com/frankbardon/parsec/rpc"
	"github.com/frankbardon/parsec/service"
)

// mockIdP is the same shape used by auth/oidc_test.go but lives in
// the server tests so the surface assertions can mint tokens with
// the right audience.
type mockIdP struct {
	srv    *httptest.Server
	priv   *rsa.PrivateKey
	kid    string
	issuer string
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &mockIdP{priv: priv, kid: "srv-kid"}
	mux := http.NewServeMux()
	idp.srv = httptest.NewServer(mux)
	idp.issuer = idp.srv.URL
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.issuer,
			"jwks_uri":                              idp.issuer + "/jwks",
			"authorization_endpoint":                idp.issuer + "/authz",
			"token_endpoint":                        idp.issuer + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(priv.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kid": idp.kid, "kty": "RSA", "alg": "RS256", "use": "sig", "n": n, "e": e,
			}},
		})
	})
	return idp
}

func (idp *mockIdP) close() { idp.srv.Close() }

func (idp *mockIdP) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "kid": idp.kid, "typ": "JWT"}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(claims)
	h := base64.RawURLEncoding.EncodeToString(hb)
	p := base64.RawURLEncoding.EncodeToString(pb)
	signing := h + "." + p
	hash := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.priv, 5, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// startOIDCServer boots a parsec instance wired with the supplied
// OIDC config + an HTTP surface using MgmtValidator. Returns the
// httptest.Server, the parsec instance (for issuing HMAC tokens), and
// a cleanup.
func startOIDCServer(t *testing.T, cfg *auth.OIDCConfig) (*httptest.Server, *parsec.Parsec, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := auth.NewKeyRingFromSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsec.New(parsec.Options{
		KeyRing:       ring,
		OIDCConfig:    cfg,
		SweepInterval: 50 * time.Millisecond,
		Logger:        slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(doneCh)
	}()
	time.Sleep(75 * time.Millisecond)
	svc := service.New(p, "test-0.0.0")
	srv := httptest.NewServer(server.New(p, svc, slog.New(slog.DiscardHandler), server.MgmtValidator(p)))
	cleanup := func() {
		srv.Close()
		cancel()
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
			t.Logf("warn: parsec.Run did not exit within 2s")
		}
	}
	return srv, p, cleanup
}

func TestHTTP_OIDC_AcceptsIDToken(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	cfg := &auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-srv", SubjectClaim: "email"}
	srv, _, stop := startOIDCServer(t, cfg)
	defer stop()

	tok := idp.sign(t, map[string]any{
		"iss":   idp.issuer,
		"aud":   "parsec-srv",
		"sub":   "user-42",
		"email": "ops@example.com",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	req, _ := http.NewRequest(http.MethodPost, srv.URL+rpc.ParsecServicePathPrefix+"ListChannels", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, body)
	}
}

func TestHTTP_OIDC_AcceptsHMACToken(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	cfg := &auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-srv"}
	srv, p, stop := startOIDCServer(t, cfg)
	defer stop()

	tok, _, err := p.Issuer().IssueMgmt("operator", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+rpc.ParsecServicePathPrefix+"ListChannels", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, body)
	}
}

func TestHTTP_OIDC_RejectsBogusToken(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	cfg := &auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-srv"}
	srv, _, stop := startOIDCServer(t, cfg)
	defer stop()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+rpc.ParsecServicePathPrefix+"ListChannels", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer not.a.token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHTTP_OIDC_ManifestAdvertisesIssuer(t *testing.T) {
	idp := newMockIdP(t)
	defer idp.close()
	cfg := &auth.OIDCConfig{Issuer: idp.issuer, Audience: "parsec-srv"}
	srv, _, stop := startOIDCServer(t, cfg)
	defer stop()

	resp, err := http.Get(srv.URL + "/manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var env struct {
		Payload struct {
			AuthOIDCEnabled bool   `json:"auth_oidc_enabled"`
			AuthOIDCIssuer  string `json:"auth_oidc_issuer"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode manifest: %v body=%s", err, body)
	}
	if !env.Payload.AuthOIDCEnabled {
		t.Errorf("manifest auth_oidc_enabled = false, want true")
	}
	if env.Payload.AuthOIDCIssuer != idp.issuer {
		t.Errorf("manifest auth_oidc_issuer = %q, want %q", env.Payload.AuthOIDCIssuer, idp.issuer)
	}
}
