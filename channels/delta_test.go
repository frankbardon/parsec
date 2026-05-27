package channels

import (
	"testing"
	"time"
)

func TestMemoryStore_SetDeltaPersists(t *testing.T) {
	s := NewMemoryStore()
	n, _ := ParseName("public:test.system.status")
	if _, _, err := s.OpenPublic(n, time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDelta(n, true); err != nil {
		t.Fatal(err)
	}
	ch, err := s.Get(n)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.DeltaEnabled {
		t.Fatal("DeltaEnabled should be true after SetDelta")
	}
}

func TestMemoryStore_SetDeltaUnknownRejected(t *testing.T) {
	s := NewMemoryStore()
	n, _ := ParseName("public:test.system.status")
	if err := s.SetDelta(n, true); err == nil {
		t.Fatal("expected ChannelNotFound on unknown channel")
	}
}

func TestManager_DefaultDeltaDisabled(t *testing.T) {
	m := NewManager()
	n, _ := ParseName("public:test.system.status")
	if _, err := m.OpenPublic(n, time.Minute); err != nil {
		t.Fatal(err)
	}
	if m.IsDelta(n) {
		t.Fatal("default channel should NOT be delta-enabled")
	}
}

func TestManager_EnableDeltaFlips(t *testing.T) {
	m := NewManager()
	n, _ := ParseName("public:scoreboard.counter.x")
	if _, err := m.OpenPublic(n, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := m.SetDelta(n, true); err != nil {
		t.Fatal(err)
	}
	if !m.IsDelta(n) {
		t.Fatal("delta should be enabled")
	}
	if err := m.SetDelta(n, false); err != nil {
		t.Fatal(err)
	}
	if m.IsDelta(n) {
		t.Fatal("delta should be disabled")
	}
}
