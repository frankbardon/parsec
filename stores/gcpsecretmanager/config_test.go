package gcpsecretmanager

import (
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "project id is required",
			cfg:     Config{},
			wantErr: "ProjectID is required",
		},
		{
			name:    "secret id is checked against the API's own rule",
			cfg:     Config{ProjectID: "p", SecretID: "not/a/secret"},
			wantErr: "must match",
		},
		{
			name:    "one credential source at a time",
			cfg:     Config{ProjectID: "p", CredentialsFile: "sa.json", CredentialsJSON: []byte("{}")},
			wantErr: "not both",
		},
		{
			name:    "negative retention is a typo, not a policy",
			cfg:     Config{ProjectID: "p", RetainVersions: -1},
			wantErr: "must not be negative",
		},
		{
			name: "defaults fill in",
			cfg:  Config{ProjectID: "p"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			err := cfg.validate()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("validate() = %v, want an error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if cfg.SecretID != DefaultSecretID {
				t.Fatalf("SecretID = %q, want %q", cfg.SecretID, DefaultSecretID)
			}
			if cfg.ReconcileInterval != DefaultReconcileInterval {
				t.Fatalf("ReconcileInterval = %s, want %s", cfg.ReconcileInterval, DefaultReconcileInterval)
			}
			if cfg.LockTTL != DefaultLockTTL || cfg.LockWait != DefaultLockWait {
				t.Fatalf("lock defaults = %s/%s, want %s/%s", cfg.LockTTL, cfg.LockWait, DefaultLockTTL, DefaultLockWait)
			}
			if cfg.NodeID == "" {
				t.Fatal("NodeID was left empty")
			}
		})
	}
}

func TestConfigNegativeReconcileSurvivesValidate(t *testing.T) {
	// Negative means "do not poll" — validate must not helpfully turn it
	// back into the default, or a single-writer deployment that opted out
	// silently starts polling again.
	cfg := Config{ProjectID: "p", ReconcileInterval: -1}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.ReconcileInterval != -1 {
		t.Fatalf("ReconcileInterval = %s, want it left at -1", cfg.ReconcileInterval)
	}
}

func TestClientOptionsPreferJSONOverFile(t *testing.T) {
	cfg := Config{ProjectID: "p", CredentialsJSON: []byte("{}")}
	if got := len(cfg.clientOptions()); got != 1 {
		t.Fatalf("clientOptions() = %d options, want 1", got)
	}
	none := Config{ProjectID: "p"}
	if got := len(none.clientOptions()); got != 0 {
		t.Fatalf("clientOptions() without credentials = %d options, want 0 (ADC)", got)
	}
}

func TestSanitizeNodeID(t *testing.T) {
	tests := map[string]string{
		"host.example.com":       "host-example-com",
		"pod:1":                  "pod-1",
		"":                       "node",
		strings.Repeat("a", 100): strings.Repeat("a", 64),
		"parsec-node_3":          "parsec-node_3",
	}
	for in, want := range tests {
		if got := sanitizeNodeID(in); got != want {
			t.Fatalf("sanitizeNodeID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLeaseRoundTrip(t *testing.T) {
	at := time.Now().Add(30 * time.Second).Truncate(time.Millisecond)
	in := lease{owner: "node-7", expires: at}
	out, ok := parseLease(in.encode())
	if !ok {
		t.Fatalf("parseLease(%q) failed", in.encode())
	}
	if out.owner != in.owner || !out.expires.Equal(at) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
	// An unparseable annotation must not be honored as a lease: a typo
	// would otherwise block every rotation in the fleet forever.
	for _, bad := range []string{"", "no-colon", "node:not-a-number", ":12345"} {
		if _, ok := parseLease(bad); ok {
			t.Fatalf("parseLease(%q) reported a lease", bad)
		}
	}
}

func TestParseVersionNumber(t *testing.T) {
	n, err := parseVersionNumber("projects/p/secrets/s/versions/42")
	if err != nil || n != 42 {
		t.Fatalf("parseVersionNumber = %d, %v; want 42, nil", n, err)
	}
	if _, err := parseVersionNumber("projects/p/secrets/s"); err == nil {
		t.Fatal("parseVersionNumber accepted a secret name")
	}
	if _, err := parseVersionNumber("projects/p/secrets/s/versions/latest"); err == nil {
		t.Fatal("parseVersionNumber accepted an unresolved alias")
	}
}
