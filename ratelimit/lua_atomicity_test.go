package ratelimit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frankbardon/parsec/ratelimit"
)

// TestLua_AtomicityUnderConcurrency hits the same Redis-backed limiter
// key from N concurrent goroutines and asserts that the total number of
// Allowed responses equals max_count exactly — i.e. the sliding-window
// Lua script does not oversubscribe under contention.
//
// Skips when Redis is unreachable.
func TestLua_AtomicityUnderConcurrency(t *testing.T) {
	prefix := "parsec-ratelimit-test-lua"
	c, cleanup := newRedisClient(t, prefix)
	defer cleanup()

	const ceiling = 50
	const goroutines = 200
	lim := ratelimit.NewRedisLimiter(c, ratelimit.Limit{Rate: ceiling, Per: 5 * time.Second}).WithKeyPrefix(prefix)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			dec, err := lim.Allow(context.Background(), "publish:atomic", 1)
			if err != nil {
				t.Errorf("Allow err: %v", err)
				return
			}
			if dec.Allowed {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := allowed.Load(); got != ceiling {
		t.Fatalf("expected exactly %d allowed (no over- or under-subscription), got %d", ceiling, got)
	}
}
