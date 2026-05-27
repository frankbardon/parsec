package config

import (
	"testing"
	"time"

	"github.com/frankbardon/parsec"
)

func TestResolve_FullConfig(t *testing.T) {
	cfg := &Config{
		Server: ServerSection{Addr: ":9000"},
		Auth: AuthSection{
			StateDir:    "/var/lib/parsec",
			MgmtSubject: "ops",
			MgmtTTL:     "12h",
			KeyringPoll: "10s",
		},
		Redis: RedisSection{
			Addr:      "localhost:6379",
			KeyPrefix: "parsec-staging",
			NodeID:    "node-7",
		},
		Manager: ManagerSection{SweepInterval: "60s"},
		SinkRetry: SinkRetrySection{
			MaxAttempts: 7,
			BaseBackoff: "2s",
			MaxBackoff:  "60s",
			PerSink: map[string]RetryEntry{
				"slack": {MaxAttempts: 3, BaseBackoff: "500ms"},
			},
		},
		RateLimits: RateLimitsSection{
			Publish: BucketSection{Rate: 100, Per: "1s", Burst: 30},
		},
		Observability: ObservabilitySection{
			MetricsBearerToken: "secret",
			OTLPEndpoint:       "https://otlp.example.com",
			TrustedProxies:     []string{"10.0.0.0/8", "192.168.0.0/16"},
		},
	}
	cfg.ApplyDefaults()
	r, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	if r.Addr != ":9000" {
		t.Errorf("addr: %q", r.Addr)
	}
	if r.MgmtTTL != 12*time.Hour {
		t.Errorf("mgmt_ttl: %v", r.MgmtTTL)
	}
	if r.RedisAddr != "localhost:6379" {
		t.Errorf("redis addr: %q", r.RedisAddr)
	}
	if r.SinkRetry.MaxAttempts != 7 || r.SinkRetry.BaseBackoff != 2*time.Second {
		t.Errorf("retry resolved wrong: %+v", r.SinkRetry)
	}
	if r.PerSinkRetry["slack"].MaxAttempts != 3 {
		t.Errorf("per-sink slack: %d", r.PerSinkRetry["slack"].MaxAttempts)
	}
	if r.RateLimits.Publish.Rate != 100 || r.RateLimits.Publish.Per != time.Second {
		t.Errorf("publish: %+v", r.RateLimits.Publish)
	}
	if len(r.TrustedProxies) != 2 {
		t.Errorf("trusted_proxies: %d", len(r.TrustedProxies))
	}
}

func TestResolve_ApplyToOnlyOverridesSetFields(t *testing.T) {
	cfg := &Config{
		Auth:    AuthSection{StateDir: "/var/lib/parsec"},
		Redis:   RedisSection{Addr: "localhost:6379"},
		Manager: ManagerSection{SweepInterval: "60s"},
	}
	cfg.ApplyDefaults()
	r, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	opts := parsec.Options{
		KeyringPollInterval: 99 * time.Second, // pre-set; config has empty default
	}
	r.ApplyTo(&opts)

	if opts.StateDir == "" {
		t.Error("state_dir should be applied")
	}
	if opts.RedisAddr != "localhost:6379" {
		t.Errorf("redis addr should be applied: %q", opts.RedisAddr)
	}
	if opts.SweepInterval != 60*time.Second {
		t.Errorf("sweep_interval: %v", opts.SweepInterval)
	}
	// KeyringPoll was pre-set on Options AND config has 5s default;
	// ApplyTo's KeyringPoll > 0 check means the 5s default DOES
	// overwrite. That's the documented behavior: any non-zero resolved
	// value wins. Verify expected behavior.
	if opts.KeyringPollInterval != 5*time.Second {
		t.Errorf("keyring_poll: want 5s (from default), got %v", opts.KeyringPollInterval)
	}
}

