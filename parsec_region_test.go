package parsec_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/service"
)

// TestOptionsRegion_PropagatesToManifest is the multi-region smoke
// test: setting Options.Region must show up as a manifest field, and
// Options.Peers must round-trip too. Single-region (zero-value) mode
// must omit both fields entirely so single-region manifests stay lean.
func TestOptionsRegion_PropagatesToManifest(t *testing.T) {
	cases := []struct {
		name     string
		region   string
		peers    []string
		wantRgn  string
		wantPeer []string
	}{
		{
			name:    "single_region_omits_fields",
			region:  "",
			peers:   nil,
			wantRgn: "",
		},
		{
			name:     "multi_region_propagates_label",
			region:   "us-east",
			peers:    []string{"https://parsec.eu-west.example.com:8000", "https://parsec.ap-south.example.com:8000"},
			wantRgn:  "us-east",
			wantPeer: []string{"https://parsec.eu-west.example.com:8000", "https://parsec.ap-south.example.com:8000"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
				SweepInterval: 50 * time.Millisecond,
				Logger:        slog.New(slog.DiscardHandler),
				Region:        tc.region,
				Peers:         tc.peers,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			doneCh := make(chan struct{})
			go func() { _ = p.Run(ctx); close(doneCh) }()
			time.Sleep(75 * time.Millisecond)
			defer func() {
				cancel()
				<-doneCh
			}()
			if got := p.Region(); got != tc.region {
				t.Errorf("p.Region() = %q, want %q", got, tc.region)
			}
			if got, want := len(p.Peers()), len(tc.peers); got != want {
				t.Errorf("len(p.Peers()) = %d, want %d", got, want)
			}
			svc := service.New(p, "test")
			env := svc.Manifest(ctx)
			body, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			var probe struct {
				Payload struct {
					Region string   `json:"region"`
					Peers  []string `json:"peers"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(body, &probe); err != nil {
				t.Fatal(err)
			}
			if probe.Payload.Region != tc.wantRgn {
				t.Errorf("manifest.region = %q, want %q", probe.Payload.Region, tc.wantRgn)
			}
			if len(probe.Payload.Peers) != len(tc.wantPeer) {
				t.Fatalf("len(manifest.peers) = %d, want %d", len(probe.Payload.Peers), len(tc.wantPeer))
			}
			for i, p := range tc.wantPeer {
				if probe.Payload.Peers[i] != p {
					t.Errorf("manifest.peers[%d] = %q, want %q", i, probe.Payload.Peers[i], p)
				}
			}
		})
	}
}

// TestOptionsPeers_DefensiveCopy ensures the manifest never returns
// the caller-owned slice, so a mutation after parsec.New cannot
// retroactively change reported peers.
func TestOptionsPeers_DefensiveCopy(t *testing.T) {
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := auth.NewKeyRingFromSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	peers := []string{"https://a.example.com:8000"}
	p, err := parsec.New(parsec.Options{
		KeyRing: ring,
		Logger:  slog.New(slog.DiscardHandler),
		Peers:   peers,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := p.Peers()
	got[0] = "tampered"
	if p.Peers()[0] != "https://a.example.com:8000" {
		t.Fatalf("Peers() returns a shared slice; tampering leaked: %v", p.Peers())
	}
}
