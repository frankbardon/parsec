package sinks_test

import (
	"strings"
	"testing"

	"github.com/frankbardon/parsec/sinks"
	_ "github.com/frankbardon/parsec/sinks/email"
	_ "github.com/frankbardon/parsec/sinks/slack"
	_ "github.com/frankbardon/parsec/sinks/webhook"
)

func TestFactory_BuiltinKindsRegistered(t *testing.T) {
	kinds := sinks.FactoryKinds()
	want := []string{"email", "slack", "webhook"}
	for _, w := range want {
		if _, ok := sinks.LookupFactory(w); !ok {
			t.Errorf("kind %q not registered (have: %v)", w, kinds)
		}
	}
}

func TestFactory_BuildEmail(t *testing.T) {
	s, err := sinks.BuildSink("email", "alerts", map[string]any{
		"smtp_addr": "smtp.example.com:587",
		"from":      "ops@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "alerts" {
		t.Errorf("name should match the user-supplied key, got %q", s.Name())
	}
}

func TestFactory_BuildSlack(t *testing.T) {
	s, err := sinks.BuildSink("slack", "ops-chan", map[string]any{
		"webhook_url": "https://hooks.slack.com/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "ops-chan" {
		t.Errorf("name should match the user-supplied key, got %q", s.Name())
	}
}

func TestFactory_BuildWebhook(t *testing.T) {
	s, err := sinks.BuildSink("webhook", "partner", map[string]any{
		"url": "https://partner.example.com/hook",
		"headers": map[string]any{
			"X-Tenant": "us-east",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "partner" {
		t.Errorf("name: %q", s.Name())
	}
}

func TestFactory_MissingRequiredField(t *testing.T) {
	_, err := sinks.BuildSink("email", "alerts", map[string]any{
		// no smtp_addr, no from
	})
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("expected required-field error, got %v", err)
	}
}

func TestFactory_UnknownKind(t *testing.T) {
	_, err := sinks.BuildSink("not-a-kind", "x", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("expected unknown-kind error, got %v", err)
	}
}
