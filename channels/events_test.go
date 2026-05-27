package channels

import (
	"testing"
	"time"
)

func drainOne(t *testing.T, ch <-chan Event, want EventKind, wantName string) Event {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Kind != want {
			t.Fatalf("kind: want %q got %q", want, ev.Kind)
		}
		if ev.Name.String() != wantName {
			t.Fatalf("name: want %q got %q", wantName, ev.Name.String())
		}
		return ev
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for %s event on %s", want, wantName)
		return Event{}
	}
}

func TestEvents_OpenPublicEmitsOpened(t *testing.T) {
	m := NewManager()
	out := make(chan Event, 4)
	m.Subscribe(out)

	n, _ := ParseName("public:webapp.system.status")
	if _, err := m.OpenPublic(n, time.Minute); err != nil {
		t.Fatal(err)
	}
	drainOne(t, out, EventOpened, n.String())
}

func TestEvents_CreatePrivateEmitsOpened(t *testing.T) {
	m := NewManager()
	out := make(chan Event, 4)
	m.Subscribe(out)

	n, _ := ParseName("private:webapp.user.42.downloads")
	if _, err := m.CreatePrivate(n, time.Minute); err != nil {
		t.Fatal(err)
	}
	drainOne(t, out, EventOpened, n.String())
}

func TestEvents_DeleteEmitsDeleted(t *testing.T) {
	m := NewManager()
	out := make(chan Event, 4)
	m.Subscribe(out)

	n, _ := ParseName("public:webapp.system.status")
	_, _ = m.OpenPublic(n, time.Minute)
	<-out // drain opened
	if err := m.Delete(n); err != nil {
		t.Fatal(err)
	}
	drainOne(t, out, EventDeleted, n.String())
}

func TestEvents_DeleteIdempotent(t *testing.T) {
	m := NewManager()
	out := make(chan Event, 4)
	m.Subscribe(out)

	n, _ := ParseName("public:webapp.system.status")
	_, _ = m.OpenPublic(n, time.Minute)
	<-out
	_ = m.Delete(n)
	<-out
	_ = m.Delete(n) // already deleted; must NOT emit
	select {
	case ev := <-out:
		t.Fatalf("unexpected event on redundant delete: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestEvents_SweepPublicEmitsClosed(t *testing.T) {
	m := NewManager()
	out := make(chan Event, 4)
	m.Subscribe(out)

	n, _ := ParseName("public:webapp.system.status")
	ch, _ := m.OpenPublic(n, 50*time.Millisecond)
	<-out // drain opened
	m.Sweep(ch.LastActive.Add(time.Second))
	drainOne(t, out, EventClosed, n.String())
}

func TestEvents_SweepPrivateEmitsDeleted(t *testing.T) {
	m := NewManager()
	out := make(chan Event, 4)
	m.Subscribe(out)

	n, _ := ParseName("private:webapp.user.42.downloads")
	ch, _ := m.CreatePrivate(n, 50*time.Millisecond)
	<-out // drain opened
	m.Sweep(ch.LastActive.Add(time.Second))
	drainOne(t, out, EventDeleted, n.String())
}

func TestEvents_ReopenClosedEmitsOpened(t *testing.T) {
	m := NewManager()
	out := make(chan Event, 8)
	m.Subscribe(out)

	n, _ := ParseName("public:webapp.system.status")
	ch, _ := m.OpenPublic(n, 50*time.Millisecond)
	<-out
	m.Sweep(ch.LastActive.Add(time.Second))
	<-out // closed
	if _, err := m.OpenPublic(n, time.Minute); err != nil {
		t.Fatal(err)
	}
	drainOne(t, out, EventOpened, n.String())
}

func TestEvents_NonBlockingOnFullBuffer(t *testing.T) {
	m := NewManager()
	full := make(chan Event) // unbuffered
	m.Subscribe(full)
	// must NOT block — drop on full
	done := make(chan struct{})
	go func() {
		n, _ := ParseName("public:webapp.system.status")
		_, _ = m.OpenPublic(n, time.Minute)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manager blocked on slow subscriber")
	}
}

func TestEvents_IsOpen(t *testing.T) {
	m := NewManager()
	n, _ := ParseName("public:webapp.system.status")
	if m.IsOpen(n) {
		t.Fatal("unknown should be closed")
	}
	_, _ = m.OpenPublic(n, time.Minute)
	if !m.IsOpen(n) {
		t.Fatal("opened should be open")
	}
	_ = m.Delete(n)
	if m.IsOpen(n) {
		t.Fatal("deleted should not be open")
	}
}
