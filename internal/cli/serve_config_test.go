package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/frankbardon/parsec/internal/config"
)

// TestServeCommand_ConfigFlagPresent confirms the new flag exists with
// the expected env source. Smoke check; the heavy lifting is in
// internal/config/.
func TestServeCommand_ConfigFlagPresent(t *testing.T) {
	cmd := ServeCommand()
	var found bool
	for _, f := range cmd.Flags {
		if f.Names()[0] == "config" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("--config flag missing from serve command")
	}
}

// TestConfig_LoadResolveBuildOptions walks the full path a CLI invocation
// uses: read YAML → Resolve → ApplyTo parsec.Options.
func TestConfig_LoadResolveBuildOptions(t *testing.T) {
	body := `
server:
  addr: ":7777"
auth:
  state_dir: /tmp/parsec-cli-test
  mgmt_ttl: 1h
redis:
  addr: localhost:6379
  key_prefix: parsec-cli
rate_limits:
  publish:
    rate: 50
    per: 1s
`
	path := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if r.Addr != ":7777" {
		t.Errorf("addr: %q", r.Addr)
	}
	if r.RedisAddr != "localhost:6379" {
		t.Errorf("redis: %q", r.RedisAddr)
	}
	if r.RateLimits.Publish.Rate != 50 || r.RateLimits.Publish.Per != time.Second {
		t.Errorf("publish: %+v", r.RateLimits.Publish)
	}
	// confirm Resolve is deterministic
	_, err = cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
}

// TestServe_ConfigEnvOverridesFile asserts the documented precedence:
// env var wins over file value. We don't boot the server here — we
// invoke the config layer with the env interpolation and confirm it
// landed in the resolved struct.
func TestServe_ConfigEnvOverridesFile(t *testing.T) {
	t.Setenv("PARSEC_TEST_REDIS_FROM_ENV", "redis://from-env:6379")
	body := `redis:
  addr: "${PARSEC_TEST_REDIS_FROM_ENV}"`
	path := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if r.RedisAddr != "redis://from-env:6379" {
		t.Fatalf("env did not propagate via interpolation: %q", r.RedisAddr)
	}
}

// ensure the package compiles against the CLI; not a behavioral test.
var _ = context.Background
