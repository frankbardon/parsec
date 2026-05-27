package channels

import (
	"testing"
	"time"
)

func TestManager_OpenPublic_ReopensClosed(t *testing.T) {
	m := NewManager()
	n, _ := ParseName("public:webapp.system.status")
	ch, err := m.OpenPublic(n, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ch.State != StateOpen {
		t.Fatal("expected open")
	}
	future := ch.LastActive.Add(time.Second)
	m.Sweep(future)
	got, _ := m.Get(n)
	if got.State != StateClosed {
		t.Fatalf("expected closed, got %s", got.State)
	}
	if _, err := m.OpenPublic(n, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestManager_CreatePrivate_TTLCap(t *testing.T) {
	m := NewManager()
	n, _ := ParseName("private:webapp.session.abc.notifications")
	if _, err := m.CreatePrivate(n, 2*time.Hour); err == nil {
		t.Fatal("expected ttl cap rejection")
	}
}

func TestManager_CreatePrivate_AutoDeletes(t *testing.T) {
	m := NewManager()
	n, _ := ParseName("private:webapp.session.abc.notifications")
	ch, err := m.CreatePrivate(n, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	future := ch.LastActive.Add(time.Second)
	m.Sweep(future)
	if _, err := m.Get(n); err == nil {
		t.Fatal("expected private channel to be deleted after TTL")
	}
}

func TestManager_CreatePrivate_DuplicateRejected(t *testing.T) {
	m := NewManager()
	n, _ := ParseName("private:webapp.session.abc.notifications")
	if _, err := m.CreatePrivate(n, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreatePrivate(n, time.Minute); err == nil {
		t.Fatal("expected duplicate rejection")
	}
}

func TestManager_Touch(t *testing.T) {
	m := NewManager()
	n, _ := ParseName("public:webapp.system.status")
	if _, err := m.OpenPublic(n, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := m.Touch(n); err != nil {
		t.Fatal(err)
	}
}
