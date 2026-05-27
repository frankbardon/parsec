package cache

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/frankbardon/parsec/envelope"
)

func sample(seq int64) envelope.Envelope {
	return envelope.Envelope{
		Channel: "public:app.dom.id", Sequence: seq,
		ProducedAt: time.Now().UTC(),
		Producer:   envelope.Producer{ID: "p", Kind: envelope.ProducerService},
		Aspect:     "data", Payload: json.RawMessage(`{}`),
	}
}

func TestMemoryCacheBasic(t *testing.T) {
	c := NewMemoryCache(4)
	defer c.Close()
	ctx := context.Background()
	if err := c.Put(ctx, "k", sample(1), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(ctx, "k")
	if err != nil || !ok || got.Sequence != 1 {
		t.Fatalf("hit: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := c.Get(ctx, "missing"); ok {
		t.Fatal("expected miss")
	}
	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Get(ctx, "k"); ok {
		t.Fatal("expected miss after delete")
	}
}

func TestMemoryCacheLRU(t *testing.T) {
	c := NewMemoryCache(2)
	defer c.Close()
	ctx := context.Background()
	_ = c.Put(ctx, "a", sample(1), 0)
	_ = c.Put(ctx, "b", sample(2), 0)
	// promote a
	_, _, _ = c.Get(ctx, "a")
	_ = c.Put(ctx, "c", sample(3), 0) // evicts b
	if _, ok, _ := c.Get(ctx, "b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok, _ := c.Get(ctx, "a"); !ok {
		t.Fatal("a should still be present")
	}
	st := c.Stats()
	if st.Evictions == 0 {
		t.Fatalf("evictions: %+v", st)
	}
}

func TestMemoryCacheExpiry(t *testing.T) {
	c := NewMemoryCache(4)
	defer c.Close()
	now := time.Now()
	c.clock = func() time.Time { return now }
	ctx := context.Background()
	_ = c.Put(ctx, "k", sample(1), time.Second)
	now = now.Add(2 * time.Second)
	if _, ok, _ := c.Get(ctx, "k"); ok {
		t.Fatal("expired entry returned")
	}
}

func TestTenantScoping(t *testing.T) {
	c := NewMemoryCache(8)
	defer c.Close()
	ctx := context.Background()
	_ = c.PutForTenant(ctx, "t1", "k", sample(1), 0)
	_ = c.PutForTenant(ctx, "t2", "k", sample(2), 0)
	got, _, _ := c.GetForTenant(ctx, "t1", "k")
	if got.Sequence != 1 {
		t.Fatalf("t1: %d", got.Sequence)
	}
	got, _, _ = c.GetForTenant(ctx, "t2", "k")
	if got.Sequence != 2 {
		t.Fatalf("t2: %d", got.Sequence)
	}
	// Cross-tenant must miss.
	if _, ok, _ := c.Get(ctx, "k"); ok {
		t.Fatal("untenanted Get should not see tenant-scoped entry")
	}
}

func TestRedisCacheRoundTrip(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	c := NewRedisCache(client, "test")
	ctx := context.Background()
	if err := c.Put(ctx, "k", sample(42), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Get(ctx, "k")
	if err != nil || !ok || got.Sequence != 42 {
		t.Fatalf("hit: %+v %v %v", got, ok, err)
	}
	mr.FastForward(2 * time.Minute)
	if _, ok, _ := c.Get(ctx, "k"); ok {
		t.Fatal("expected miss after ttl")
	}
}

func TestHitRate(t *testing.T) {
	s := Stats{Hits: 8, Misses: 2}
	if r := s.HitRate(); r != 80 {
		t.Fatalf("hit rate: %v", r)
	}
	if (Stats{}).HitRate() != 0 {
		t.Fatal("empty rate should be 0")
	}
}
