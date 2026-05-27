package sinks

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	perr "github.com/frankbardon/parsec/errors"
)

func TestMemoryDLQ_PushAndItemsRoundTrip(t *testing.T) {
	d := NewMemoryDLQ()
	ctx := context.Background()
	if err := d.Push(ctx, DLQItem{Sink: "email", Message: Message{Subject: "first"}, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := d.Push(ctx, DLQItem{Sink: "email", Message: Message{Subject: "second"}, At: time.Now().Add(time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	items, err := d.Items(ctx, "email", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// newest first
	if items[0].Message.Subject != "second" {
		t.Errorf("ordering wrong; got %q first", items[0].Message.Subject)
	}
	if items[0].ID == "" {
		t.Error("ID should be assigned on Push")
	}
}

func TestMemoryDLQ_CountAndDiscard(t *testing.T) {
	d := NewMemoryDLQ()
	ctx := context.Background()
	_ = d.Push(ctx, DLQItem{Sink: "email", Message: Message{Subject: "a"}})
	_ = d.Push(ctx, DLQItem{Sink: "slack", Message: Message{Subject: "b"}})
	n, _ := d.Count(ctx, "email")
	if n != 1 {
		t.Errorf("email count = %d, want 1", n)
	}
	n, _ = d.Count(ctx, "")
	if n != 2 {
		t.Errorf("total count = %d, want 2", n)
	}
	// fetch one to get its ID
	items, _ := d.Items(ctx, "email", 1)
	id := items[0].ID
	if err := d.Discard(ctx, id); err != nil {
		t.Fatal(err)
	}
	n, _ = d.Count(ctx, "email")
	if n != 0 {
		t.Errorf("email count after discard = %d, want 0", n)
	}
}

func TestMemoryDLQ_ReplayCallsInnerSink(t *testing.T) {
	called := atomic.Int32{}
	d := NewMemoryDLQ()
	d.SetRegistryLookup(func(name string) Sink {
		return &replaySink{name: name, calls: &called}
	})
	ctx := context.Background()
	_ = d.Push(ctx, DLQItem{Sink: "email", Message: Message{Subject: "x"}})
	items, _ := d.Items(ctx, "email", 1)
	if err := d.Replay(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if called.Load() != 1 {
		t.Errorf("expected 1 replay call, got %d", called.Load())
	}
}

func TestMemoryDLQ_ReplayMissingID(t *testing.T) {
	d := NewMemoryDLQ()
	d.SetRegistryLookup(func(string) Sink { return nil })
	err := d.Replay(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for missing id")
	}
	var pe *perr.Error
	if !asErr(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
}

type replaySink struct {
	name  string
	calls *atomic.Int32
}

func (r *replaySink) Name() string { return r.name }
func (r *replaySink) Send(_ context.Context, _ Recipient, _ Message) error {
	r.calls.Add(1)
	return nil
}

// asErr is a tiny helper to avoid importing errors twice across test files.
func asErr(err error, target **perr.Error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*perr.Error); ok {
		*target = e
		return true
	}
	return false
}
