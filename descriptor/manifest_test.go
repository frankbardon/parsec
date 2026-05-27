package descriptor

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewEnvelope_SetsFields(t *testing.T) {
	before := time.Now().UTC()
	env := NewEnvelope("parsec.test", map[string]string{"hello": "world"})
	after := time.Now().UTC()

	if env.FormatVersion != FormatVersion {
		t.Errorf("FormatVersion = %q, want %q", env.FormatVersion, FormatVersion)
	}
	if env.Kind != "parsec.test" {
		t.Errorf("Kind = %q", env.Kind)
	}
	if env.GeneratedAt.Before(before) || env.GeneratedAt.After(after) {
		t.Errorf("GeneratedAt %v outside [%v,%v]", env.GeneratedAt, before, after)
	}
	if env.Payload == nil {
		t.Fatal("Payload nil")
	}
}

func TestEnvelope_RoundTripJSON(t *testing.T) {
	original := NewEnvelope("parsec.test", map[string]any{"n": 42.0, "s": "x"})
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var got Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.FormatVersion != original.FormatVersion {
		t.Errorf("FormatVersion mismatch: %q vs %q", got.FormatVersion, original.FormatVersion)
	}
	if got.Kind != original.Kind {
		t.Errorf("Kind mismatch: %q vs %q", got.Kind, original.Kind)
	}
	if !got.GeneratedAt.Equal(original.GeneratedAt) {
		t.Errorf("GeneratedAt mismatch: %v vs %v", got.GeneratedAt, original.GeneratedAt)
	}
	// The payload comes back as map[string]any.
	got_payload, ok := got.Payload.(map[string]any)
	if !ok {
		t.Fatalf("Payload not map: %T", got.Payload)
	}
	if got_payload["s"] != "x" || got_payload["n"] != 42.0 {
		t.Errorf("payload round-trip mismatch: %#v", got_payload)
	}
}

func TestWriteEnvelope_AppendsNewline(t *testing.T) {
	var buf bytes.Buffer
	env := NewEnvelope("k", map[string]string{"a": "b"})
	if err := WriteEnvelope(&buf, env); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("WriteEnvelope output should end with newline; got %q", out)
	}
	// Indented JSON.
	if !strings.Contains(out, "  ") {
		t.Errorf("WriteEnvelope output should be indented; got %q", out)
	}
}

func TestManifest_JSONShape(t *testing.T) {
	m := Manifest{
		Service:       "parsec",
		Version:       "1.2.3",
		GoVersion:     "go1.26",
		Surfaces:      []string{"cli", "twirp", "websocket"},
		Sinks:         []string{"email"},
		Visibilities:  []string{"public", "private"},
		MaxPrivateTTL: "1h0m0s",
		KeyRotation:   "supported",
		ActiveKeyID:   "k-abc",
		Persistence:   "in-memory",
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"service", "version", "go_version", "surfaces", "sinks", "visibilities",
		"max_private_ttl", "key_rotation", "active_key_id", "persistence",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("manifest JSON missing key %q (raw=%s)", key, raw)
		}
	}
	if decoded["service"] != "parsec" {
		t.Errorf("service field = %v, want parsec", decoded["service"])
	}
}

func TestManifest_OmitsEmpty(t *testing.T) {
	m := Manifest{Service: "parsec", Version: "v", Persistence: "in-memory"}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "go_version") {
		t.Errorf("empty go_version should be omitted: %s", raw)
	}
	if strings.Contains(string(raw), "active_key_id") {
		t.Errorf("empty active_key_id should be omitted: %s", raw)
	}
}

func TestFormatVersionConstant(t *testing.T) {
	if FormatVersion == "" {
		t.Fatal("FormatVersion empty")
	}
}
