package client

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/frankbardon/parsec/envelope"
	"github.com/frankbardon/parsec/schema"
)

func newTestClient(t *testing.T, tr Transport) *Client {
	t.Helper()
	c, err := New(
		WithTransport(tr),
		WithProducer(envelope.Producer{ID: "tester", Kind: envelope.ProducerService}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPublishStampsAndSequences(t *testing.T) {
	tr := NewMemoryTransport()
	c := newTestClient(t, tr)
	defer c.Close()
	ctx := context.Background()
	for i := range 3 {
		err := c.Publish(ctx, "public:a.b.c", envelope.Envelope{
			Aspect:  "data",
			Payload: json.RawMessage(`{"n":` + itoa(i) + `}`),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	raws, err := tr.History(ctx, "public:a.b.c", HistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(raws) != 3 {
		t.Fatalf("history: %d", len(raws))
	}
	for i, r := range raws {
		env, err := envelope.Decode(r)
		if err != nil {
			t.Fatal(err)
		}
		if env.Sequence != int64(i+1) {
			t.Fatalf("seq[%d] = %d", i, env.Sequence)
		}
		if env.Producer.ID != "tester" {
			t.Fatalf("producer: %s", env.Producer.ID)
		}
	}
}

func TestSubscribeDispatch(t *testing.T) {
	tr := NewMemoryTransport()
	pub := newTestClient(t, tr)
	defer pub.Close()
	sub := newTestClient(t, tr)
	defer sub.Close()

	ctx := context.Background()
	s, err := sub.Subscribe(ctx, "public:a.b.c")
	if err != nil {
		t.Fatal(err)
	}
	var got []envelope.Envelope
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	s.OnEnvelope(func(e envelope.Envelope) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
		wg.Done()
	})

	if err := pub.Publish(ctx, "public:a.b.c", envelope.Envelope{Aspect: "data", Payload: json.RawMessage(`"hi"`)}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(ctx, "public:a.b.c", envelope.Envelope{Aspect: "cursor", Payload: json.RawMessage(`"pos"`)}); err != nil {
		t.Fatal(err)
	}
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("handlers did not fire")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].Aspect != "data" || got[1].Aspect != "cursor" {
		t.Fatalf("order: %v", got)
	}
}

func TestSubscribeAspectFilter(t *testing.T) {
	tr := NewMemoryTransport()
	pub := newTestClient(t, tr)
	defer pub.Close()
	sub := newTestClient(t, tr)
	defer sub.Close()
	ctx := context.Background()
	s, err := sub.Subscribe(ctx, "public:a.b.c", WithAspectFilter("data"))
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	s.OnEnvelope(func(e envelope.Envelope) {
		mu.Lock()
		seen = append(seen, e.Aspect)
		mu.Unlock()
		wg.Done()
	})
	_ = pub.Publish(ctx, "public:a.b.c", envelope.Envelope{Aspect: "cursor", Payload: json.RawMessage(`"x"`)})
	_ = pub.Publish(ctx, "public:a.b.c", envelope.Envelope{Aspect: "data", Payload: json.RawMessage(`"y"`)})
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("data handler never fired")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "data" {
		t.Fatalf("filtered: %v", seen)
	}
}

func TestOnAspectTyped(t *testing.T) {
	tr := NewMemoryTransport()
	pub := newTestClient(t, tr)
	defer pub.Close()
	sub := newTestClient(t, tr)
	defer sub.Close()
	ctx := context.Background()
	type Shape struct {
		Text string `json:"text"`
	}
	s, err := sub.Subscribe(ctx, "public:a.b.c")
	if err != nil {
		t.Fatal(err)
	}
	var got Shape
	done := make(chan struct{})
	OnAspectTyped(s, "msg", func(v Shape, _ envelope.Envelope) {
		got = v
		close(done)
	})
	body, _ := json.Marshal(Shape{Text: "hello"})
	if err := pub.Publish(ctx, "public:a.b.c", envelope.Envelope{Aspect: "msg", Payload: body}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("typed handler never fired")
	}
	if got.Text != "hello" {
		t.Fatalf("typed: %+v", got)
	}
}

func TestCausationPropagation(t *testing.T) {
	tr := NewMemoryTransport()
	pub := newTestClient(t, tr)
	defer pub.Close()
	sub := newTestClient(t, tr)
	defer sub.Close()
	ctx := context.Background()

	s, err := sub.Subscribe(ctx, "public:a.b.c")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	s.OnAspect("parent", func(env envelope.Envelope) {
		_ = s.PublishDerived(ctx, "child", json.RawMessage(`{}`))
		close(done)
	})
	if err := pub.Publish(ctx, "public:a.b.c", envelope.Envelope{Aspect: "parent", Payload: json.RawMessage(`"x"`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("parent handler never fired")
	}
	// History should contain parent then child; child's causation
	// points at parent.
	raws, _ := tr.History(ctx, "public:a.b.c", HistoryOptions{})
	if len(raws) < 2 {
		t.Fatalf("history: %d", len(raws))
	}
	child, _ := envelope.Decode(raws[len(raws)-1])
	if child.Causation.ParentChannel != "public:a.b.c" || child.Causation.ParentSequence == 0 {
		t.Fatalf("causation not set: %+v", child.Causation)
	}
}

func TestValidatorRejectsBadPayload(t *testing.T) {
	tr := NewMemoryTransport()
	reg := schema.NewMemoryRegistry()
	additionalFalse := false
	_ = reg.Register(schema.ChannelPattern{
		Pattern: "public:a.b.c",
		Aspects: map[string]schema.Aspect{
			"data": {Name: "data", PayloadSchema: &schema.JSONSchema{
				Type:                 "object",
				Required:             []string{"text"},
				AdditionalProperties: &additionalFalse,
				Properties:           map[string]*schema.JSONSchema{"text": {Type: "string"}},
			}},
		},
	})
	pub := newTestClient(t, tr)
	defer pub.Close()
	sub, err := New(
		WithTransport(tr),
		WithProducer(envelope.Producer{ID: "sub", Kind: envelope.ProducerService}),
		WithValidator(&schema.Validator{Registry: reg, Mode: schema.ModeStrict}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Connect(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx := context.Background()
	s, err := sub.Subscribe(ctx, "public:a.b.c")
	if err != nil {
		t.Fatal(err)
	}
	var received int
	var mu sync.Mutex
	s.OnEnvelope(func(envelope.Envelope) { mu.Lock(); received++; mu.Unlock() })

	// Bad payload — should be dropped.
	_ = pub.Publish(ctx, "public:a.b.c", envelope.Envelope{Aspect: "data", Payload: json.RawMessage(`{"text":42}`)})
	// Good payload — should pass.
	_ = pub.Publish(ctx, "public:a.b.c", envelope.Envelope{Aspect: "data", Payload: json.RawMessage(`{"text":"ok"}`)})
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if received != 1 {
		t.Fatalf("received %d; expected 1 (bad dropped, good kept)", received)
	}
}

func TestSequenceSnapshotter(t *testing.T) {
	store := &memSnap{}
	store.data = []byte(`{"public:a.b.c":5}`)
	c, err := New(
		WithTransport(NewMemoryTransport()),
		WithProducer(envelope.Producer{ID: "p", Kind: envelope.ProducerService}),
		WithSnapshotter(store),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Publish(context.Background(), "public:a.b.c", envelope.Envelope{Aspect: "x", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// next sequence should be 6, persisted to snap
	if !mapContains(store.last, "public:a.b.c", 6) {
		t.Fatalf("snapshot not updated: %v", store.last)
	}
}

func mapContains(m map[string]int64, k string, v int64) bool { return m[k] == v }

type memSnap struct {
	data []byte
	last map[string]int64
}

func (m *memSnap) Save(s map[string]int64) error {
	m.last = s
	b, _ := json.Marshal(s)
	m.data = b
	return nil
}

func (m *memSnap) Load() (map[string]int64, error) {
	var out map[string]int64
	if err := json.Unmarshal(m.data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
