package sinks

import (
	"context"
	"sort"
	"testing"
)

// memSink is a tiny in-memory Sink used to exercise the registry plumbing.
type memSink struct {
	name string
	got  []Message
}

func (m *memSink) Name() string { return m.name }
func (m *memSink) Send(_ context.Context, _ Recipient, msg Message) error {
	m.got = append(m.got, msg)
	return nil
}

func TestRegistry_EmptyByDefault(t *testing.T) {
	r := NewRegistry()
	if got := r.Names(); len(got) != 0 {
		t.Fatalf("expected empty registry, got %v", got)
	}
	if got := r.Get("nope"); got != nil {
		t.Fatalf("Get on empty should return nil, got %v", got)
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	s := &memSink{name: "email"}
	r.Register(s)
	got := r.Get("email")
	if got == nil {
		t.Fatal("Get returned nil after Register")
	}
	if got.Name() != "email" {
		t.Errorf("Get returned wrong sink: %s", got.Name())
	}
}

func TestRegistry_LastWriteWins(t *testing.T) {
	r := NewRegistry()
	r.Register(&memSink{name: "email"})
	second := &memSink{name: "email"}
	r.Register(second)
	if r.Get("email") != Sink(second) {
		t.Fatal("expected last Register to win")
	}
}

func TestRegistry_Names(t *testing.T) {
	r := NewRegistry()
	r.Register(&memSink{name: "email"})
	r.Register(&memSink{name: "slack"})
	r.Register(&memSink{name: "webhook"})
	names := r.Names()
	sort.Strings(names)
	want := []string{"email", "slack", "webhook"}
	if len(names) != len(want) {
		t.Fatalf("Names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("Names[%d] = %s, want %s", i, names[i], want[i])
		}
	}
}

func TestSink_RoundTripThroughInterface(t *testing.T) {
	var s Sink = &memSink{name: "x"}
	if err := s.Send(context.Background(), nil, Message{Subject: "hi", Body: "world"}); err != nil {
		t.Fatal(err)
	}
	ms := s.(*memSink)
	if len(ms.got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(ms.got))
	}
	if ms.got[0].Subject != "hi" {
		t.Errorf("Subject mismatch: %q", ms.got[0].Subject)
	}
}
