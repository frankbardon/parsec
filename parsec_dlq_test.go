package parsec

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/sinks"
)

// alwaysFailSink fails every call with a transient error. Counts attempts.
type alwaysFailSink struct {
	calls atomic.Int32
}

func (s *alwaysFailSink) Name() string { return "always-fail" }
func (s *alwaysFailSink) Send(_ context.Context, _ sinks.Recipient, _ sinks.Message) error {
	s.calls.Add(1)
	return sinks.Transient(perr.New(perr.SinkUnavailable, "boom"))
}

// successAfterFailSink fails the first N calls then succeeds. Used to verify
// Replay drives a sink to success.
type successAfterFailSink struct {
	name    string
	failsAt atomic.Int32 // how many remaining calls should fail
	calls   atomic.Int32
}

func (s *successAfterFailSink) Name() string { return s.name }
func (s *successAfterFailSink) Send(_ context.Context, _ sinks.Recipient, _ sinks.Message) error {
	s.calls.Add(1)
	if s.failsAt.Add(-1) >= 0 {
		return sinks.Transient(perr.New(perr.SinkUnavailable, "blip"))
	}
	return nil
}

func TestParsec_PublishOrSink_LandsTerminalFailureInDLQ(t *testing.T) {
	secret, _ := auth.GenerateSecret()
	reg := sinks.NewRegistry()
	failing := &alwaysFailSink{}
	reg.Register(failing)
	p, err := New(Options{
		KeyRing:    ringFromSecret(t, secret),
		Sinks:      reg,
		SinkRetry:  sinks.RetryConfig{MaxAttempts: 3, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, JitterFraction: 0.01},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	time.Sleep(75 * time.Millisecond)

	if _, err := p.OpenPublic("public:test.dlq.demo", time.Minute); err != nil {
		t.Fatal(err)
	}
	delivered, err := p.PublishOrSink(ctx, "public:test.dlq.demo", []byte("payload"),
		"always-fail", nil, sinks.Message{Subject: "drop", Body: "test"})
	if err != nil {
		t.Fatalf("expected DLQ to swallow err, got %v", err)
	}
	if !delivered {
		t.Errorf("delivered = false, want true (sink invoked because zero subscribers)")
	}
	if failing.calls.Load() != 3 {
		t.Errorf("expected 3 attempts before DLQ, got %d", failing.calls.Load())
	}
	items, err := p.DLQ().Items(ctx, "always-fail", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 DLQ item, got %d", len(items))
	}
	if items[0].Message.Subject != "drop" {
		t.Errorf("Subject = %q", items[0].Message.Subject)
	}
	cancel()
	<-done
}

func TestParsec_DLQReplay_DrivesSinkToSuccess(t *testing.T) {
	secret, _ := auth.GenerateSecret()
	reg := sinks.NewRegistry()
	s := &successAfterFailSink{name: "flaky"}
	s.failsAt.Store(0) // never fail — replay should hit a successful path
	reg.Register(s)
	p, err := New(Options{
		KeyRing:    ringFromSecret(t, secret),
		Sinks:      reg,
		SinkRetry:  sinks.RetryConfig{MaxAttempts: 1, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, JitterFraction: 0.01},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()
	time.Sleep(75 * time.Millisecond)

	// Manually push a synthetic item into the DLQ then call Replay.
	if err := p.DLQ().Push(ctx, sinks.DLQItem{
		Sink:    "flaky",
		At:      time.Now(),
		Message: sinks.Message{Subject: "replay-me"},
	}); err != nil {
		t.Fatal(err)
	}
	items, _ := p.DLQ().Items(ctx, "flaky", 1)
	if len(items) == 0 {
		t.Fatal("no items pushed")
	}
	if err := p.DLQ().Replay(ctx, items[0].ID); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if s.calls.Load() != 1 {
		t.Errorf("expected 1 sink call from Replay, got %d", s.calls.Load())
	}
}
