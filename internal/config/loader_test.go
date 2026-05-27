package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "parsec.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_HappyPath(t *testing.T) {
	body := `
server:
  addr: ":9000"
auth:
  state_dir: /var/lib/parsec
  mgmt_subject: ops
  mgmt_ttl: 12h
redis:
  addr: localhost:6379
  key_prefix: parsec-staging
manager:
  sweep_interval: 60s
sink_retry:
  max_attempts: 7
  base_backoff: 2s
  per_sink:
    slack:
      max_attempts: 3
rate_limits:
  publish:
    rate: 100
    per: 1s
    burst: 30
observability:
  metrics_bearer_token: secret
  trusted_proxies:
    - 10.0.0.0/8
`
	cfg, err := Load(writeTempConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":9000" {
		t.Errorf("addr: %q", cfg.Server.Addr)
	}
	if cfg.Auth.MgmtTTL != "12h" {
		t.Errorf("mgmt_ttl: %q", cfg.Auth.MgmtTTL)
	}
	if cfg.SinkRetry.MaxAttempts != 7 {
		t.Errorf("retry max_attempts: %d", cfg.SinkRetry.MaxAttempts)
	}
	if cfg.SinkRetry.PerSink["slack"].MaxAttempts != 3 {
		t.Errorf("per-sink slack: %d", cfg.SinkRetry.PerSink["slack"].MaxAttempts)
	}
	if cfg.RateLimits.Publish.Rate != 100 {
		t.Errorf("publish rate: %d", cfg.RateLimits.Publish.Rate)
	}
}

func TestLoad_DefaultsAppliedForOmittedFields(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, "server: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":8000" {
		t.Errorf("default addr: %q", cfg.Server.Addr)
	}
	if cfg.Auth.MgmtSubject != "operator" {
		t.Errorf("default mgmt_subject: %q", cfg.Auth.MgmtSubject)
	}
	if cfg.Manager.SweepInterval != "30s" {
		t.Errorf("default sweep_interval: %q", cfg.Manager.SweepInterval)
	}
}

func TestLoad_UnknownKeysRejected(t *testing.T) {
	body := `
server:
  addr: ":8000"
  typo_field: boom
`
	_, err := Load(writeTempConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}


func TestLoad_InvalidDuration(t *testing.T) {
	body := `auth:
  mgmt_ttl: "12 hours"`
	_, err := Load(writeTempConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "auth.mgmt_ttl") {
		t.Fatalf("expected mgmt_ttl error, got %v", err)
	}
}

func TestLoad_EnvInterpolation(t *testing.T) {
	t.Setenv("PARSEC_TEST_REDIS", "redis://localhost:6379")
	body := `redis:
  addr: "${PARSEC_TEST_REDIS}"`
	cfg, err := Load(writeTempConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Redis.Addr != "redis://localhost:6379" {
		t.Fatalf("interp failed: %q", cfg.Redis.Addr)
	}
}

func TestLoad_BucketRateRequiresPer(t *testing.T) {
	body := `rate_limits:
  publish:
    rate: 100`
	_, err := Load(writeTempConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "rate_limits.publish.per") {
		t.Fatalf("expected per-required error, got %v", err)
	}
}

// TestLoad_RegionAndPeers exercises the top-level multi-region keys.
// Both surface in the manifest unchanged; the loader treats them as
// pass-through fields.
func TestLoad_RegionAndPeers(t *testing.T) {
	body := `region: us-east
peers:
  - https://parsec.eu-west.example.com:8000
  - https://parsec.ap-south.example.com:8000`
	cfg, err := Load(writeTempConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "us-east" {
		t.Errorf("region = %q, want us-east", cfg.Region)
	}
	if len(cfg.Peers) != 2 {
		t.Fatalf("peers len = %d, want 2", len(cfg.Peers))
	}
	if cfg.Peers[0] != "https://parsec.eu-west.example.com:8000" {
		t.Errorf("peers[0] = %q", cfg.Peers[0])
	}
}
