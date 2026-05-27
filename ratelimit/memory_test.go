package ratelimit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frankbardon/parsec/ratelimit"
)

func TestMemory_UnlimitedReturnsAlwaysAllowed(t *testing.T) {
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{}) // zero == unlimited
	for i := 0; i < 100; i++ {
		dec, err := lim.Allow(context.Background(), "alice", 1)
		if err != nil {
			t.Fatalf("iter=%d err=%v", i, err)
		}
		if !dec.Allowed {
			t.Fatalf("iter=%d unexpectedly denied", i)
		}
	}
}

func TestMemory_BasicBudget(t *testing.T) {
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 3, Per: time.Second})
	for i := 0; i < 3; i++ {
		dec, err := lim.Allow(context.Background(), "alice", 1)
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Allowed {
			t.Fatalf("iter=%d should be allowed (budget=3)", i)
		}
	}
	dec, err := lim.Allow(context.Background(), "alice", 1)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Allowed {
		t.Fatal("4th call should be denied; budget=3")
	}
	if dec.Reset <= 0 {
		t.Errorf("expected non-zero Reset, got %v", dec.Reset)
	}
}

func TestMemory_BurstAcceptsThenDrains(t *testing.T) {
	// 10/s with burst 30 — 30 immediately, then 31st denied.
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 10, Per: time.Second, Burst: 30})
	for i := 0; i < 30; i++ {
		dec, err := lim.Allow(context.Background(), "alice", 1)
		if err != nil {
			t.Fatal(err)
		}
		if !dec.Allowed {
			t.Fatalf("burst iter=%d should be allowed", i)
		}
	}
	dec, _ := lim.Allow(context.Background(), "alice", 1)
	if dec.Allowed {
		t.Fatal("31st call should be denied")
	}
}

func TestMemory_DistinctKeysHaveDistinctBudgets(t *testing.T) {
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 1, Per: time.Second})
	// alice's budget should not affect bob.
	d1, _ := lim.Allow(context.Background(), "alice", 1)
	if !d1.Allowed {
		t.Fatal("alice 1st should pass")
	}
	d2, _ := lim.Allow(context.Background(), "bob", 1)
	if !d2.Allowed {
		t.Fatal("bob 1st should pass (separate bucket)")
	}
	d3, _ := lim.Allow(context.Background(), "alice", 1)
	if d3.Allowed {
		t.Fatal("alice 2nd should be denied (budget exhausted)")
	}
}

func TestMemory_WindowDrop(t *testing.T) {
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 2, Per: 100 * time.Millisecond})
	current := time.Now()
	lim.SetClock(func() time.Time { return current })
	for i := 0; i < 2; i++ {
		dec, _ := lim.Allow(context.Background(), "k", 1)
		if !dec.Allowed {
			t.Fatalf("iter=%d should be allowed", i)
		}
	}
	dec, _ := lim.Allow(context.Background(), "k", 1)
	if dec.Allowed {
		t.Fatal("3rd should be denied")
	}
	// Advance past the window; entries drop, budget refreshes.
	current = current.Add(200 * time.Millisecond)
	dec, _ = lim.Allow(context.Background(), "k", 1)
	if !dec.Allowed {
		t.Fatal("after window expiry the bucket should accept again")
	}
}

func TestMemory_ContextCancel(t *testing.T) {
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 1, Per: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := lim.Allow(ctx, "k", 1)
	if err == nil {
		t.Fatal("expected ctx.Err() to propagate")
	}
}

func TestMemory_ConcurrentSafe(t *testing.T) {
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 1000, Per: time.Second, Burst: 1000})
	var wg sync.WaitGroup
	var allowed atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				dec, _ := lim.Allow(context.Background(), "k", 1)
				if dec.Allowed {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 1000 {
		t.Fatalf("expected 1000 allowed (ceiling=1000 across 100 goroutines x 10 calls), got %d", allowed.Load())
	}
}

func TestMemory_SweepEvictsExpired(t *testing.T) {
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 1, Per: 10 * time.Millisecond})
	cur := time.Now()
	lim.SetClock(func() time.Time { return cur })
	for i := 0; i < 5; i++ {
		_, _ = lim.Allow(context.Background(), "k"+string(rune('a'+i)), 1)
	}
	// All five entries are inside the window; sweep should not remove.
	if removed := lim.Sweep(); removed != 0 {
		t.Errorf("expected 0 removed before window expiry, got %d", removed)
	}
	cur = cur.Add(20 * time.Millisecond)
	if removed := lim.Sweep(); removed != 5 {
		t.Errorf("expected 5 removed after window expiry, got %d", removed)
	}
}

func TestLimit_Normalize(t *testing.T) {
	tests := []struct {
		name     string
		in, want ratelimit.Limit
	}{
		{"zero-stays-zero", ratelimit.Limit{}, ratelimit.Limit{}},
		{"rate-default-per", ratelimit.Limit{Rate: 5}, ratelimit.Limit{Rate: 5, Per: time.Second, Burst: 5}},
		{"burst-floor-is-rate", ratelimit.Limit{Rate: 5, Per: time.Second, Burst: 1}, ratelimit.Limit{Rate: 5, Per: time.Second, Burst: 5}},
		{"negative-clamped", ratelimit.Limit{Rate: -1, Per: -1, Burst: -1}, ratelimit.Limit{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Normalize()
			if got != tc.want {
				t.Errorf("Normalize(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
