package sinks

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func redisDLQAddrFromEnv() string {
	addr := os.Getenv("PARSEC_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	return addr
}

func newRedisDLQClient(t *testing.T) redis.UniversalClient {
	t.Helper()
	addr := redisDLQAddrFromEnv()
	c := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	return c
}

func freshRedisDLQ(t *testing.T) (*RedisDLQ, redis.UniversalClient) {
	t.Helper()
	c := newRedisDLQClient(t)
	prefix := "parsec-dlqtest-" + t.Name()
	d := NewRedisDLQ(c).WithKeyPrefix(prefix)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		// Clean up every stream we created during the test.
		for _, k := range d.allStreams() {
			c.Del(ctx, k)
		}
		_ = c.Close()
	})
	return d, c
}

func TestRedisDLQ_PushAndItems(t *testing.T) {
	d, _ := freshRedisDLQ(t)
	ctx := context.Background()
	if err := d.Push(ctx, DLQItem{Sink: "email", Message: Message{Subject: "hi"}, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	items, err := d.Items(ctx, "email", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].ID == "" {
		t.Error("ID should come back populated")
	}
	if items[0].Sink != "email" {
		t.Errorf("Sink = %q", items[0].Sink)
	}
}

func TestRedisDLQ_CountAndDiscard(t *testing.T) {
	d, _ := freshRedisDLQ(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = d.Push(ctx, DLQItem{Sink: "x", Message: Message{Subject: "n"}, At: time.Now()})
	}
	n, err := d.Count(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}
	items, _ := d.Items(ctx, "x", 5)
	if err := d.Discard(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	n, _ = d.Count(ctx, "x")
	if n != 2 {
		t.Errorf("Count after discard = %d, want 2", n)
	}
}

func TestRedisDLQ_MaxLenCapsStream(t *testing.T) {
	d, _ := freshRedisDLQ(t)
	d.WithMaxLen(5)
	ctx := context.Background()
	// MAXLEN ~ trims in chunks (Redis radix-tree granularity). Push enough
	// items to make trimming inevitable — at 200 the macro nodes will have
	// rolled and the stream length will be bounded near the cap.
	for i := 0; i < 200; i++ {
		_ = d.Push(ctx, DLQItem{Sink: "cap", Message: Message{Subject: "n"}, At: time.Now()})
	}
	n, _ := d.Count(ctx, "cap")
	if n > 150 {
		t.Errorf("Count = %d, expected MAXLEN to trim aggressively below 150", n)
	}
	if n < 1 {
		t.Errorf("Count = %d, expected some items retained", n)
	}
}

func TestRedisDLQ_Replay(t *testing.T) {
	d, _ := freshRedisDLQ(t)
	called := atomic.Int32{}
	d.SetRegistryLookup(func(name string) Sink {
		return &replaySink{name: name, calls: &called}
	})
	ctx := context.Background()
	_ = d.Push(ctx, DLQItem{Sink: "x", Message: Message{Subject: "h"}, At: time.Now()})
	items, _ := d.Items(ctx, "x", 1)
	if err := d.Replay(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if called.Load() != 1 {
		t.Errorf("expected 1 replay call, got %d", called.Load())
	}
	// Replay does NOT discard.
	n, _ := d.Count(ctx, "x")
	if n != 1 {
		t.Errorf("Replay should not remove the item; Count = %d, want 1", n)
	}
}

// TestRedisDLQ_RecipientRoundTrip verifies that Recipient JSON survives the
// stream round-trip — the recipient comes back as json.RawMessage so the
// sink's DecodeRecipient can rebuild the typed value.
func TestRedisDLQ_RecipientRoundTrip(t *testing.T) {
	d, _ := freshRedisDLQ(t)
	ctx := context.Background()
	type myRecipient struct {
		Addr string `json:"address"`
	}
	_ = d.Push(ctx, DLQItem{
		Sink:      "email",
		Recipient: myRecipient{Addr: "x@example.com"},
		Message:   Message{Subject: "rt"},
		At:        time.Now(),
	})
	items, _ := d.Items(ctx, "email", 1)
	raw, ok := items[0].Recipient.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage, got %T", items[0].Recipient)
	}
	var got myRecipient
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Addr != "x@example.com" {
		t.Errorf("Recipient.Addr = %q", got.Addr)
	}
}
