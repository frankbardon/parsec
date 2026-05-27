package sinks

import (
	"context"
	stderrors "errors"
	"sync/atomic"
	"testing"
	"time"

	perr "github.com/frankbardon/parsec/errors"
)

// counterSink fails fail-count times with the configured error, then succeeds.
type counterSink struct {
	name      string
	failures  int32
	err       error
	calls     atomic.Int32
	got       atomic.Int32
	lastRecip Recipient
	lastMsg   Message
}

func (s *counterSink) Name() string { return s.name }
func (s *counterSink) Send(_ context.Context, to Recipient, m Message) error {
	n := s.calls.Add(1)
	s.lastRecip = to
	s.lastMsg = m
	if int32(n) <= s.failures {
		return s.err
	}
	s.got.Add(1)
	return nil
}

func TestRetryConfig_Normalize_FillsDefaults(t *testing.T) {
	c := RetryConfig{}.Normalize()
	if c.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d", c.MaxAttempts)
	}
	if c.BaseBackoff != time.Second {
		t.Errorf("BaseBackoff = %v", c.BaseBackoff)
	}
	if c.MaxBackoff != 30*time.Second {
		t.Errorf("MaxBackoff = %v", c.MaxBackoff)
	}
	if c.JitterFraction != 0.2 {
		t.Errorf("JitterFraction = %v", c.JitterFraction)
	}
}

func TestRetryConfig_BackoffFor_GrowsAndCaps(t *testing.T) {
	c := RetryConfig{MaxAttempts: 10, BaseBackoff: 100 * time.Millisecond, MaxBackoff: 500 * time.Millisecond, JitterFraction: 0.0}.Normalize()
	if c.BackoffFor(1) != 0 {
		t.Errorf("attempt 1 should be zero")
	}
	// attempt 2 → ~100ms, attempt 3 → ~200ms ... attempt N → capped at 500ms
	b2 := c.BackoffFor(2)
	if b2 < 80*time.Millisecond || b2 > 120*time.Millisecond {
		t.Errorf("attempt 2 backoff out of bounds: %v", b2)
	}
	bBig := c.BackoffFor(8)
	if bBig > 600*time.Millisecond {
		t.Errorf("attempt 8 should be capped near 500ms, got %v", bBig)
	}
}

func TestRetryConfig_BackoffFor_JitterStaysInBounds(t *testing.T) {
	c := RetryConfig{MaxAttempts: 5, BaseBackoff: 100 * time.Millisecond, MaxBackoff: time.Second, JitterFraction: 0.2}.Normalize()
	for i := 0; i < 50; i++ {
		b := c.BackoffFor(2)
		// nominal 100ms with +/- 20% → 80ms..120ms
		if b < 75*time.Millisecond || b > 125*time.Millisecond {
			t.Errorf("iteration %d: backoff %v outside jitter envelope", i, b)
		}
	}
}

func TestRetrier_TransientThenSuccess(t *testing.T) {
	inner := &counterSink{name: "x", failures: 3, err: Transient(perr.New(perr.SinkUnavailable, "blip"))}
	dlq := NewMemoryDLQ()
	r := NewRetrier(inner, RetryConfig{MaxAttempts: 5, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, JitterFraction: 0.01}, dlq)
	err := r.Send(context.Background(), nil, Message{Subject: "s"})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if inner.calls.Load() != 4 {
		t.Errorf("expected 4 calls (3 fail + 1 success), got %d", inner.calls.Load())
	}
	count, _ := dlq.Count(context.Background(), "x")
	if count != 0 {
		t.Errorf("DLQ should be empty on success, got %d", count)
	}
}

func TestRetrier_TransientExhaustsThenDLQ(t *testing.T) {
	inner := &counterSink{name: "x", failures: 100, err: Transient(perr.New(perr.SinkUnavailable, "always blip"))}
	dlq := NewMemoryDLQ()
	r := NewRetrier(inner, RetryConfig{MaxAttempts: 3, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, JitterFraction: 0.01}, dlq)
	err := r.Send(context.Background(), nil, Message{Subject: "s"})
	if err != nil {
		t.Fatalf("DLQ push should swallow the error, got %v", err)
	}
	if inner.calls.Load() != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", inner.calls.Load())
	}
	items, _ := dlq.Items(context.Background(), "x", 10)
	if len(items) != 1 {
		t.Fatalf("expected 1 DLQ item, got %d", len(items))
	}
	if items[0].Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", items[0].Attempts)
	}
	if items[0].LastError == "" {
		t.Error("LastError should be populated")
	}
}

func TestRetrier_TerminalGoesStraightToDLQ(t *testing.T) {
	inner := &counterSink{name: "x", failures: 100, err: Terminal(perr.New(perr.InvalidArgument, "bad recipient"))}
	dlq := NewMemoryDLQ()
	r := NewRetrier(inner, RetryConfig{MaxAttempts: 5, BaseBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond, JitterFraction: 0.01}, dlq)
	err := r.Send(context.Background(), nil, Message{Subject: "s"})
	if err != nil {
		t.Fatalf("DLQ push should swallow the error, got %v", err)
	}
	if inner.calls.Load() != 1 {
		t.Errorf("terminal error must not retry; got %d calls", inner.calls.Load())
	}
	count, _ := dlq.Count(context.Background(), "x")
	if count != 1 {
		t.Errorf("expected 1 DLQ item, got %d", count)
	}
}

func TestRetrier_ContextCancellationAbortsLoop(t *testing.T) {
	inner := &counterSink{name: "x", failures: 100, err: Transient(perr.New(perr.SinkUnavailable, "blip"))}
	dlq := NewMemoryDLQ()
	r := NewRetrier(inner, RetryConfig{MaxAttempts: 100, BaseBackoff: 50 * time.Millisecond, MaxBackoff: 500 * time.Millisecond, JitterFraction: 0.0}, dlq)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := r.Send(ctx, nil, Message{Subject: "s"})
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Errorf("context cancellation should abort fast, took %v", elapsed)
	}
	// DLQ push should have succeeded → err == nil
	if err != nil {
		t.Errorf("expected nil err after DLQ push, got %v", err)
	}
}

func TestRetrier_NoDLQ_ReturnsUnderlyingError(t *testing.T) {
	inner := &counterSink{name: "x", failures: 100, err: Terminal(perr.New(perr.InvalidArgument, "bad"))}
	r := NewRetrier(inner, RetryConfig{MaxAttempts: 2, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, JitterFraction: 0.01}, nil)
	err := r.Send(context.Background(), nil, Message{})
	if err == nil {
		t.Fatal("expected error when DLQ is nil")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
}

func TestRetrier_IsRetrier_Marker(t *testing.T) {
	r := NewRetrier(&counterSink{name: "x"}, RetryConfig{}, nil)
	if !r.IsRetrier() {
		t.Error("IsRetrier should return true")
	}
}

func TestWrapSink_IsIdempotent(t *testing.T) {
	inner := &counterSink{name: "x"}
	once := WrapSink(inner, RetryConfig{MaxAttempts: 3}, NewMemoryDLQ())
	twice := WrapSink(once, RetryConfig{MaxAttempts: 3}, NewMemoryDLQ())
	if once != twice {
		t.Error("WrapSink should be idempotent")
	}
}

func TestWrapSink_PassthroughWhenRetryDisabled(t *testing.T) {
	inner := &counterSink{name: "x"}
	got := WrapSink(inner, RetryConfig{MaxAttempts: 1}, nil)
	if got != Sink(inner) {
		t.Error("WrapSink should return inner when retry+dlq disabled")
	}
}
