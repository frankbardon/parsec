package schema

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// capturingPublish records every (channel, payload) the Publisher emits.
type capturingPublish struct {
	mu    sync.Mutex
	calls []capturedCall
	done  chan struct{}
	want  int
	err   error // returned to caller; nil = success
}

type capturedCall struct {
	Channel string
	Payload []byte
}

func newCapturingPublish(want int) *capturingPublish {
	return &capturingPublish{done: make(chan struct{}), want: want}
}

func (c *capturingPublish) fn(_ context.Context, ch string, data []byte) error {
	c.mu.Lock()
	c.calls = append(c.calls, capturedCall{Channel: ch, Payload: append([]byte(nil), data...)})
	n := len(c.calls)
	err := c.err
	c.mu.Unlock()
	if n == c.want {
		close(c.done)
	}
	return err
}

func (c *capturingPublish) snapshot() []capturedCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capturedCall, len(c.calls))
	copy(out, c.calls)
	return out
}

func TestPublisherBroadcastsLifecycle(t *testing.T) {
	reg := NewMemoryRegistry()
	cp := newCapturingPublish(3)

	ctx := t.Context()

	pub := NewPublisher(reg, cp.fn, PublisherOptions{})
	if pub.Channel() != ChangeChannel {
		t.Fatalf("default channel = %q, want %q", pub.Channel(), ChangeChannel)
	}
	go func() { _ = pub.Run(ctx) }()

	// Give Run a moment to call Subscribe before we start mutating.
	// Without this, Register may fire before the subscription is in
	// the map and the first event would be lost.
	waitFor(t, func() bool { return reg.subscriberCount() == 1 })

	p := ChannelPattern{Pattern: "sessions:{id}", Version: 1, Aspects: map[string]Aspect{"data": {Name: "data"}}}
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	p2 := p
	p2.Description = "updated"
	if err := reg.Update(p2); err != nil {
		t.Fatal(err)
	}
	if err := reg.Deregister(p.Pattern); err != nil {
		t.Fatal(err)
	}

	select {
	case <-cp.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for broadcasts, got %d", len(cp.snapshot()))
	}

	calls := cp.snapshot()
	if len(calls) != 3 {
		t.Fatalf("got %d calls, want 3", len(calls))
	}
	for _, want := range []struct {
		idx  int
		kind ChangeKind
	}{
		{0, ChangeRegistered},
		{1, ChangeUpdated},
		{2, ChangeDeregistered},
	} {
		var got Change
		if err := json.Unmarshal(calls[want.idx].Payload, &got); err != nil {
			t.Fatalf("call %d: unmarshal: %v", want.idx, err)
		}
		if got.Kind != want.kind {
			t.Fatalf("call %d: kind %q, want %q", want.idx, got.Kind, want.kind)
		}
		if got.Pattern != "sessions:{id}" {
			t.Fatalf("call %d: pattern %q", want.idx, got.Pattern)
		}
		if calls[want.idx].Channel != ChangeChannel {
			t.Fatalf("call %d: channel %q", want.idx, calls[want.idx].Channel)
		}
	}
}

func TestPublisherEnsureChannelCalledOnce(t *testing.T) {
	reg := NewMemoryRegistry()
	cp := newCapturingPublish(1)

	var ensureCalls int32
	var ensureMu sync.Mutex
	ensure := func(ch string) error {
		ensureMu.Lock()
		ensureCalls++
		ensureMu.Unlock()
		if ch != "public:custom.schemas" {
			t.Errorf("ensure called with %q", ch)
		}
		return nil
	}

	ctx := t.Context()

	pub := NewPublisher(reg, cp.fn, PublisherOptions{
		Channel:       "public:custom.schemas",
		EnsureChannel: ensure,
	})
	go func() { _ = pub.Run(ctx) }()

	waitFor(t, func() bool { return reg.subscriberCount() == 1 })

	if err := reg.Register(ChannelPattern{Pattern: "x:y", Aspects: map[string]Aspect{"a": {Name: "a"}}}); err != nil {
		t.Fatal(err)
	}
	<-cp.done

	ensureMu.Lock()
	defer ensureMu.Unlock()
	if ensureCalls != 1 {
		t.Fatalf("ensure called %d times, want 1", ensureCalls)
	}
	if got := cp.snapshot()[0].Channel; got != "public:custom.schemas" {
		t.Fatalf("publish channel %q", got)
	}
}

func TestPublisherEnsureChannelFailureAborts(t *testing.T) {
	reg := NewMemoryRegistry()
	publish := func(context.Context, string, []byte) error { return nil }

	sentinel := errors.New("boom")
	pub := NewPublisher(reg, publish, PublisherOptions{
		EnsureChannel: func(string) error { return sentinel },
	})
	err := pub.Run(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if reg.subscriberCount() != 0 {
		t.Fatalf("subscriber leaked: count=%d", reg.subscriberCount())
	}
}

func TestPublisherContextCancelExitsCleanly(t *testing.T) {
	reg := NewMemoryRegistry()
	publish := func(context.Context, string, []byte) error { return nil }
	pub := NewPublisher(reg, publish, PublisherOptions{})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- pub.Run(ctx) }()

	waitFor(t, func() bool { return reg.subscriberCount() == 1 })
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancel")
	}
	if reg.subscriberCount() != 0 {
		t.Fatalf("subscriber leaked after cancel: count=%d", reg.subscriberCount())
	}
}

func TestPublisherPublishErrorContinues(t *testing.T) {
	reg := NewMemoryRegistry()
	var seen int32
	var mu sync.Mutex
	done := make(chan struct{})
	publish := func(context.Context, string, []byte) error {
		mu.Lock()
		seen++
		n := seen
		mu.Unlock()
		if n == 2 {
			close(done)
		}
		return errors.New("network down")
	}
	pub := NewPublisher(reg, publish, PublisherOptions{})
	ctx := t.Context()
	go func() { _ = pub.Run(ctx) }()

	waitFor(t, func() bool { return reg.subscriberCount() == 1 })

	if err := reg.Register(ChannelPattern{Pattern: "a:b", Aspects: map[string]Aspect{"x": {Name: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(ChannelPattern{Pattern: "c:d", Aspects: map[string]Aspect{"x": {Name: "x"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("publisher stopped after first error; saw=%d", seen)
	}
}

func TestNewPublisherPanicsOnNilArgs(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	NewPublisher(nil, func(context.Context, string, []byte) error { return nil }, PublisherOptions{})
}

func TestNewPublisherPanicsOnNilPublish(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	NewPublisher(NewMemoryRegistry(), nil, PublisherOptions{})
}

// subscriberCount peeks at the MemoryRegistry's subscriber map. Used to
// synchronize tests that race with Run installing its Subscribe.
func (r *MemoryRegistry) subscriberCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.subscribers)
}

// waitFor polls until cond is true or 1s elapses.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("waitFor: condition never became true")
}
