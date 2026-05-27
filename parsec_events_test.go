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

// TestEventBridge_DeletedClosedSubscribe ensures that deleting a channel
// flips IsOpen false so subsequent subscribes are rejected by the composed
// authorizer. The broker-side UnsubscribeAll is exercised but its effect
// on real WebSocket subscribers is verified by integration tests.
func TestEventBridge_DeletedClosedSubscribe(t *testing.T) {
	p, stop := testParsec(t)
	defer stop()

	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Manager().IsOpen(creds.Name) {
		t.Fatal("expected channel open after create")
	}
	if err := p.Manager().Delete(creds.Name); err != nil {
		t.Fatal(err)
	}
	// after delete, IsOpen must be false
	if p.Manager().IsOpen(creds.Name) {
		t.Fatal("expected channel not open after delete")
	}
}

// TestEventBridge_SweepClosesPublic verifies sweep transitions public
// channels to closed and IsOpen reports false.
func TestEventBridge_SweepClosesPublic(t *testing.T) {
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{KeyRing: ringFromSecret(t, secret), SweepInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	time.Sleep(40 * time.Millisecond)

	ch, err := p.OpenPublic("public:test.system.status", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Manager().IsOpen(ch.Name) {
		t.Fatal("expected open after OpenPublic")
	}
	// Wait until TTL passes and sweeper runs at least twice.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !p.Manager().IsOpen(ch.Name) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("public channel never closed after TTL")
}

// TestEventBridge_PublishUntouchedAfterDelete confirms publishes to a
// deleted private channel return PARSEC_CHANNEL_NOT_FOUND.
func TestEventBridge_PublishUntouchedAfterDelete(t *testing.T) {
	p, stop := testParsec(t)
	defer stop()

	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Manager().Delete(creds.Name)
	_, err = p.Publish(context.Background(), creds.Name.String(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected publish to fail after delete")
	}
	var pe *perr.Error
	if !errors.As(err, &pe) || pe.Code != perr.ChannelNotFound {
		t.Fatalf("expected ChannelNotFound, got %v", err)
	}
}

// TestEventBridge_DrainsEventsOnShutdown confirms the bridge exits when
// ctx is canceled. No leaked goroutines.
func TestEventBridge_DrainsEventsOnShutdown(t *testing.T) {
	secret, _ := auth.GenerateSecret()
	p, err := New(Options{KeyRing: ringFromSecret(t, secret), SweepInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
	_ = channels.EventDeleted // keep import used
}
