package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/frankbardon/parsec/internal/config"
	_ "github.com/frankbardon/parsec/sinks/email"
	_ "github.com/frankbardon/parsec/sinks/slack"
	_ "github.com/frankbardon/parsec/sinks/webhook"
)

func TestConfig_SinksBuildFromYAML(t *testing.T) {
	body := `
sinks:
  alerts-email:
    kind: email
    smtp_addr: smtp.example.com:587
    from: ops@example.com
  ops-slack:
    kind: slack
    webhook_url: https://hooks.slack.com/test
  partner-webhook:
    kind: webhook
    url: https://partner.example.com/hook
    headers:
      X-Tenant: us-east
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
	if len(r.Sinks) != 3 {
		t.Fatalf("expected 3 sinks, got %d", len(r.Sinks))
	}
	names := map[string]bool{}
	for _, s := range r.Sinks {
		names[s.Name()] = true
	}
	for _, want := range []string{"alerts-email", "ops-slack", "partner-webhook"} {
		if !names[want] {
			t.Errorf("sink %q missing (have: %v)", want, names)
		}
	}
}

func TestConfig_SinkRequiresKind(t *testing.T) {
	body := `
sinks:
  bad-sink:
    smtp_addr: smtp.example.com:587
`
	path := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("expected error for sink without kind")
	}
}
