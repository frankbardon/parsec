package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A malformed redis.addr must be rejected at load. Left to the client it
// surfaces much later as a dial error naming a host that never existed,
// with nothing pointing back at the config.
func TestValidate_RejectsMalformedRedisAddr(t *testing.T) {
	for _, addr := range []string{"localhost", "redis+sentinel://a:26379", "http://x:6379"} {
		c := &Config{}
		c.Redis.Addr = addr
		c.ApplyDefaults()
		err := c.Validate()
		if err == nil {
			t.Errorf("Validate with redis.addr=%q = nil, want error", addr)
			continue
		}
		if !strings.Contains(err.Error(), "redis.addr") {
			t.Errorf("error for %q does not name redis.addr: %v", addr, err)
		}
	}
}

func TestValidate_AcceptsSupportedRedisAddrForms(t *testing.T) {
	for _, addr := range []string{
		"localhost:6379",
		"redis://redis:6379",
		"rediss://user:pw@cache.example.com:6380/2",
		"unix:///var/run/redis.sock",
	} {
		c := &Config{}
		c.Redis.Addr = addr
		c.ApplyDefaults()
		if err := c.Validate(); err != nil {
			t.Errorf("Validate with redis.addr=%q: %v", addr, err)
		}
	}
}

// Credentials with no address means redis is not on at all — the
// credentials would be silently ignored.
func TestValidate_RejectsCredentialsWithoutAddr(t *testing.T) {
	c := &Config{}
	c.Redis.Password = "hunter2"
	c.ApplyDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("Validate with credentials but no addr = nil, want error")
	}
}

func TestResolve_RedisAuthFields(t *testing.T) {
	db := 4
	c := &Config{}
	c.Redis.Addr = "redis://redis:6379"
	c.Redis.Username = "parsec"
	c.Redis.Password = "s3cret"
	c.Redis.DB = &db
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	r, err := c.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.RedisAuth.Username != "parsec" || r.RedisAuth.Password != "s3cret" {
		t.Errorf("credentials = %q/%q", r.RedisAuth.Username, r.RedisAuth.Password)
	}
	if r.RedisAuth.DB == nil || *r.RedisAuth.DB != 4 {
		t.Errorf("DB = %v, want 4", r.RedisAuth.DB)
	}
	if r.RedisAuth.TLSConfig != nil {
		t.Error("TLSConfig set without a tls section")
	}
}

// An omitted db must stay nil so it does not override the database the
// address encodes.
func TestResolve_OmittedRedisDBStaysNil(t *testing.T) {
	c := &Config{}
	c.Redis.Addr = "redis://redis:6379/7"
	c.ApplyDefaults()
	r, err := c.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.RedisAuth.DB != nil {
		t.Errorf("DB = %v, want nil when redis.db is omitted", *r.RedisAuth.DB)
	}
}

func TestResolve_RedisTLSFromCAFile(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, selfSignedCA(t), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	c := &Config{}
	c.Redis.Addr = "redis://redis:6379"
	c.Redis.TLS.Enabled = true
	c.Redis.TLS.CAFile = ca
	c.Redis.TLS.ServerName = "redis.internal"
	c.ApplyDefaults()
	r, err := c.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.RedisAuth.TLSConfig == nil {
		t.Fatal("TLSConfig nil with a tls section configured")
	}
	if r.RedisAuth.TLSConfig.RootCAs == nil {
		t.Error("RootCAs not populated from ca_file")
	}
	if got := r.RedisAuth.TLSConfig.ServerName; got != "redis.internal" {
		t.Errorf("ServerName = %q, want redis.internal", got)
	}
}

func TestResolve_RedisTLSBadCAFile(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := &Config{}
	c.Redis.Addr = "redis://redis:6379"
	c.Redis.TLS.CAFile = bad
	c.ApplyDefaults()
	if _, err := c.Resolve(); err == nil {
		t.Fatal("Resolve with an unparseable ca_file = nil, want error")
	}

	c2 := &Config{}
	c2.Redis.Addr = "redis://redis:6379"
	c2.Redis.TLS.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	c2.ApplyDefaults()
	if _, err := c2.Resolve(); err == nil {
		t.Fatal("Resolve with a missing ca_file = nil, want error")
	}
}

// selfSignedCA mints a throwaway CA certificate so the ca_file path is
// exercised against real PEM rather than a fixture.
func selfSignedCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "parsec-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
