package ratelimit_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/frankbardon/parsec/ratelimit"
)

func redisAddr() string {
	a := os.Getenv("PARSEC_REDIS_ADDR")
	if a == "" {
		a = "localhost:6379"
	}
	return a
}

// newRedisClient pings the configured Redis; skips the test if unreachable.
// Returns a fresh client and a cleanup that DELs the per-test keys and
// closes the connection.
func newRedisClient(t *testing.T, prefix string) (redis.UniversalClient, func()) {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: redisAddr()})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		t.Skipf("redis unreachable at %s: %v", redisAddr(), err)
	}
	return c, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		iter := c.Scan(ctx, 0, prefix+":rl:*", 0).Iterator()
		var keys []string
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
		}
		if len(keys) > 0 {
			c.Del(ctx, keys...)
		}
		_ = c.Close()
	}
}

func TestRedis_UnlimitedReturnsAllowed(t *testing.T) {
	prefix := "parsec-ratelimit-test-unlimited"
	c, cleanup := newRedisClient(t, prefix)
	defer cleanup()
	lim := ratelimit.NewRedisLimiter(c, ratelimit.Limit{}).WithKeyPrefix(prefix)
	for i := 0; i < 50; i++ {
		dec, err := lim.Allow(context.Background(), "publish:alice", 1)
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Allowed {
			t.Fatalf("iter=%d should be allowed (no limit)", i)
		}
	}
}

func TestRedis_BasicBudget(t *testing.T) {
	prefix := "parsec-ratelimit-test-basic"
	c, cleanup := newRedisClient(t, prefix)
	defer cleanup()
	lim := ratelimit.NewRedisLimiter(c, ratelimit.Limit{Rate: 3, Per: time.Second}).WithKeyPrefix(prefix)
	for i := 0; i < 3; i++ {
		dec, err := lim.Allow(context.Background(), "publish:alice", 1)
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Allowed {
			t.Fatalf("iter=%d allowed should be true; remaining=%d reset=%v", i, dec.Remaining, dec.Reset)
		}
	}
	dec, err := lim.Allow(context.Background(), "publish:alice", 1)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed {
		t.Fatal("4th call should be denied")
	}
	if dec.Reset <= 0 {
		t.Errorf("expected non-zero Reset on deny, got %v", dec.Reset)
	}
}

// TestRedis_CrossNodeCoherence verifies two independent limiter instances
// (simulating two parsec nodes) sharing the same Redis enforce ONE total
// budget, not two.
func TestRedis_CrossNodeCoherence(t *testing.T) {
	prefix := "parsec-ratelimit-test-cross"
	c, cleanup := newRedisClient(t, prefix)
	defer cleanup()
	limA := ratelimit.NewRedisLimiter(c, ratelimit.Limit{Rate: 4, Per: time.Second}).WithKeyPrefix(prefix)
	limB := ratelimit.NewRedisLimiter(c, ratelimit.Limit{Rate: 4, Per: time.Second}).WithKeyPrefix(prefix)
	// Alternate between A and B; total allowed must equal 4 (not 8).
	allowed := 0
	for i := 0; i < 8; i++ {
		var lim *ratelimit.RedisLimiter
		if i%2 == 0 {
			lim = limA
		} else {
			lim = limB
		}
		dec, err := lim.Allow(context.Background(), "publish:alice", 1)
		if err != nil {
			t.Fatalf("iter=%d err=%v", i, err)
		}
		if dec.Allowed {
			allowed++
		}
	}
	if allowed != 4 {
		t.Fatalf("expected 4 allowed across two limiters, got %d", allowed)
	}
}

func TestRedis_DistinctKeysHaveDistinctBudgets(t *testing.T) {
	prefix := "parsec-ratelimit-test-keys"
	c, cleanup := newRedisClient(t, prefix)
	defer cleanup()
	lim := ratelimit.NewRedisLimiter(c, ratelimit.Limit{Rate: 1, Per: time.Second}).WithKeyPrefix(prefix)
	d1, _ := lim.Allow(context.Background(), "publish:alice", 1)
	if !d1.Allowed {
		t.Fatal("alice 1st should pass")
	}
	d2, _ := lim.Allow(context.Background(), "publish:bob", 1)
	if !d2.Allowed {
		t.Fatal("bob 1st should pass (separate bucket)")
	}
	d3, _ := lim.Allow(context.Background(), "publish:alice", 1)
	if d3.Allowed {
		t.Fatal("alice 2nd should be denied")
	}
}

func TestRedis_BucketIsolation(t *testing.T) {
	// publish:alice and subscribe:alice should not share a budget.
	prefix := "parsec-ratelimit-test-buckets"
	c, cleanup := newRedisClient(t, prefix)
	defer cleanup()
	lim := ratelimit.NewRedisLimiter(c, ratelimit.Limit{Rate: 1, Per: time.Second}).WithKeyPrefix(prefix)
	d1, _ := lim.Allow(context.Background(), "publish:alice", 1)
	d2, _ := lim.Allow(context.Background(), "subscribe:alice", 1)
	if !d1.Allowed || !d2.Allowed {
		t.Fatalf("buckets should be isolated: publish=%v subscribe=%v", d1.Allowed, d2.Allowed)
	}
	d3, _ := lim.Allow(context.Background(), "publish:alice", 1)
	if d3.Allowed {
		t.Fatal("publish:alice 2nd should deny")
	}
}

func TestRedis_ContextCancel(t *testing.T) {
	prefix := "parsec-ratelimit-test-cancel"
	c, cleanup := newRedisClient(t, prefix)
	defer cleanup()
	lim := ratelimit.NewRedisLimiter(c, ratelimit.Limit{Rate: 5, Per: time.Second}).WithKeyPrefix(prefix)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := lim.Allow(ctx, "publish:x", 1)
	if err == nil {
		t.Fatal("expected ctx.Err()")
	}
}

func TestRedis_WindowDrop(t *testing.T) {
	prefix := "parsec-ratelimit-test-window"
	c, cleanup := newRedisClient(t, prefix)
	defer cleanup()
	lim := ratelimit.NewRedisLimiter(c, ratelimit.Limit{Rate: 2, Per: 200 * time.Millisecond}).WithKeyPrefix(prefix)
	for i := 0; i < 2; i++ {
		dec, err := lim.Allow(context.Background(), "publish:x", 1)
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Allowed {
			t.Fatalf("iter=%d should pass", i)
		}
	}
	dec, _ := lim.Allow(context.Background(), "publish:x", 1)
	if dec.Allowed {
		t.Fatal("3rd should deny")
	}
	time.Sleep(220 * time.Millisecond)
	dec, _ = lim.Allow(context.Background(), "publish:x", 1)
	if !dec.Allowed {
		t.Fatal("after window expiry the bucket should accept again")
	}
}
