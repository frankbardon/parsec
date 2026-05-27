package broker

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/channels"
	perr "github.com/frankbardon/parsec/errors"
)

// startBroker constructs a Broker and runs it in a goroutine. The returned
// cancel function tears the broker down and waits for shutdown.
func startBroker(t *testing.T, opts Options) (*Broker, func()) {
	t.Helper()
	b, err := New(opts)
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = b.Run(ctx)
		close(done)
	}()
	// Wait for started=true so Publish doesn't race with Run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		ok := b.started
		b.mu.RUnlock()
		if ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return b, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Logf("warn: broker.Run did not exit cleanly")
		}
	}
}

func TestBroker_Defaults(t *testing.T) {
	b, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.opts.HistorySize != 100 {
		t.Errorf("HistorySize = %d, want 100", b.opts.HistorySize)
	}
	if b.opts.PublicHistoryTTL != 5*time.Minute {
		t.Errorf("PublicHistoryTTL = %v", b.opts.PublicHistoryTTL)
	}
	if b.opts.PrivateHistoryTTL != 5*time.Minute {
		t.Errorf("PrivateHistoryTTL = %v", b.opts.PrivateHistoryTTL)
	}
	if b.opts.SubscribeAuthorizer == nil {
		t.Error("SubscribeAuthorizer should default to non-nil")
	}
	if b.Node() == nil {
		t.Error("Node should be non-nil")
	}
}

func TestBroker_Boot(t *testing.T) {
	b, stop := startBroker(t, Options{})
	defer stop()
	if b.Node() == nil {
		t.Fatal("Node is nil after start")
	}
}

func TestBroker_DoubleRunRejected(t *testing.T) {
	b, stop := startBroker(t, Options{})
	defer stop()
	// Calling Run a second time should fail because started=true.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := b.Run(ctx)
	if err == nil {
		t.Fatal("expected double-Run error")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
}

func TestBroker_PublishBeforeRunRejected(t *testing.T) {
	b, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	name, _ := channels.BuildName(channels.VisibilityPublic, "test", "x", "", "")
	_, err = b.Publish(context.Background(), name, []byte("data"), false)
	if err == nil {
		t.Fatal("expected BrokerNotReady error")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != perr.BrokerNotReady {
		t.Fatalf("code = %s, want BrokerNotReady", pe.Code)
	}
}

func TestBroker_PublishAndHistory(t *testing.T) {
	b, stop := startBroker(t, Options{HistorySize: 50})
	defer stop()
	name, _ := channels.BuildName(channels.VisibilityPublic, "test", "x", "y", "")
	res, err := b.Publish(context.Background(), name, []byte("hello"), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Offset == 0 {
		t.Errorf("expected non-zero offset, got %d", res.Offset)
	}
	if res.Epoch == "" {
		t.Errorf("expected non-empty epoch")
	}
	// Publish should land in history. Centrifuge requires an explicit
	// HistoryFilter to return publications — pass one with a high limit so
	// we get everything since offset 0.
	hist, err := b.Node().History(name.String(), centrifuge.WithHistoryFilter(centrifuge.HistoryFilter{Limit: 50}))
	if err != nil {
		t.Fatal(err)
	}
	if len(hist.Publications) != 1 {
		t.Fatalf("expected 1 publication in history, got %d", len(hist.Publications))
	}
	if string(hist.Publications[0].Data) != "hello" {
		t.Errorf("data mismatch: %q", hist.Publications[0].Data)
	}
}

func TestBroker_PresenceZeroOnNoSubscribers(t *testing.T) {
	b, stop := startBroker(t, Options{})
	defer stop()
	name, _ := channels.BuildName(channels.VisibilityPublic, "test", "x", "", "")
	got, err := b.Presence(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("expected 0 presence, got %d", got)
	}
}

func TestBroker_UnsubscribeAllNoCrashOnUnknownChannel(t *testing.T) {
	b, stop := startBroker(t, Options{})
	defer stop()
	// Should be safe to call against a channel nobody is subscribed to.
	name, _ := channels.BuildName(channels.VisibilityPublic, "test", "x", "1", "")
	b.UnsubscribeAll(name)
}

func TestBroker_HistoryTTLDifferentForVisibility(t *testing.T) {
	b, err := New(Options{
		PublicHistoryTTL:  7 * time.Minute,
		PrivateHistoryTTL: 11 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := channels.BuildName(channels.VisibilityPublic, "a", "b", "", "")
	priv, _ := channels.BuildName(channels.VisibilityPrivate, "a", "b", "1", "")
	if got := b.historyTTLFor(pub); got != 7*time.Minute {
		t.Errorf("public TTL = %v", got)
	}
	if got := b.historyTTLFor(priv); got != 11*time.Minute {
		t.Errorf("private TTL = %v", got)
	}
}

func TestDefaultSubscribeAuthorizer_AllowsPublic(t *testing.T) {
	name, _ := channels.BuildName(channels.VisibilityPublic, "a", "b", "", "")
	if err := defaultSubscribeAuthorizer(context.Background(), "", name, centrifuge.SubscribeEvent{}); err != nil {
		t.Errorf("public should be allowed: %v", err)
	}
}

func TestDefaultSubscribeAuthorizer_DeniesPrivate(t *testing.T) {
	name, _ := channels.BuildName(channels.VisibilityPrivate, "a", "b", "1", "")
	err := defaultSubscribeAuthorizer(context.Background(), "", name, centrifuge.SubscribeEvent{})
	if err == nil {
		t.Fatal("private should require an authorizer")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != perr.AuthDenied {
		t.Fatalf("code = %s, want AuthDenied", pe.Code)
	}
}

func TestBroker_CustomAuthorizerObserved(t *testing.T) {
	var called int
	custom := func(_ context.Context, _ string, _ channels.Name, _ centrifuge.SubscribeEvent) error {
		called++
		return nil
	}
	b, err := New(Options{SubscribeAuthorizer: custom})
	if err != nil {
		t.Fatal(err)
	}
	if b.opts.SubscribeAuthorizer == nil {
		t.Fatal("custom authorizer should be stored")
	}
	// Call directly (we cannot trigger a subscribe without a websocket
	// client; the integration of the authorizer with centrifuge OnConnect
	// is verified by being-installed-and-not-nil here).
	name, _ := channels.BuildName(channels.VisibilityPublic, "a", "b", "", "")
	if err := b.opts.SubscribeAuthorizer(context.Background(), "", name, centrifuge.SubscribeEvent{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected one authorizer call, got %d", called)
	}
}

func TestBroker_ShutdownCleanOnContextCancel(t *testing.T) {
	b, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	// Wait until broker.started flips.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		started := b.started
		b.mu.RUnlock()
		if started {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("broker did not shut down in 5s")
	}
}
