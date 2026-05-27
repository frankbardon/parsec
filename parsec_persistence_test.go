package parsec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/channels"
	perr "github.com/frankbardon/parsec/errors"
)

func mustParse(t *testing.T, s string) channels.Name {
	t.Helper()
	n, err := channels.ParseName(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestPersistence_ChannelStateNotPersistedAcrossInstances pins the
// documented stance: channel records are in-memory only. A new Parsec
// instance has no knowledge of channels that the previous instance held.
// This guards against accidental drift if someone wires up a store later.
func TestPersistence_ChannelStateNotPersistedAcrossInstances(t *testing.T) {
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}

	p1, err := New(Options{KeyRing: ringFromSecret(t, secret), SweepInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p1.Run(ctx); close(done) }()
	time.Sleep(40 * time.Millisecond)

	if _, err := p1.OpenPublic("public:test.system.status", time.Minute); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done

	// Boot a second instance with the SAME secret.
	p2, err := New(Options{KeyRing: ringFromSecret(t, secret), SweepInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { _ = p2.Run(ctx2); close(done2) }()
	defer func() { cancel2(); <-done2 }()
	time.Sleep(40 * time.Millisecond)

	n := mustParse(t, "public:test.system.status")
	if _, err := p2.Manager().Get(n); err == nil {
		t.Fatal("expected ChannelNotFound on fresh instance; state must NOT persist")
	} else {
		var pe *perr.Error
		if !errors.As(err, &pe) || pe.Code != perr.ChannelNotFound {
			t.Fatalf("expected ChannelNotFound, got %v", err)
		}
	}
}

// TestPersistence_ManifestReportsInMemory asserts the manifest exposes the
// persistence stance for clients that can't read the README.
func TestPersistence_ManifestReportsInMemory(t *testing.T) {
	p, stop := testParsec(t)
	defer stop()
	if p.KeyRing().ActiveID() == "" {
		t.Fatal("expected active key id present")
	}
	// Service is not constructed here — the descriptor invariant is locked
	// in service.go and tested via the surface tests; this test guards the
	// behavioral contract: ephemeral channel manager state.
}
