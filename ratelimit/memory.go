package ratelimit

import (
	"context"
	"sync"
	"time"
)

// MemoryLimiter is a per-process sliding-window limiter. Each key gets a
// ring of event timestamps; entries older than the window are dropped on
// every check. It is the default when Parsec runs without Redis.
//
// MemoryLimiter is safe for concurrent use. The store grows with the
// active-key cardinality — call Sweep periodically (or rely on the
// implicit drop-on-touch) to keep the map bounded. For deployments
// expecting many distinct keys, prefer RedisLimiter.
type MemoryLimiter struct {
	limit Limit

	mu      sync.Mutex
	buckets map[string]*memBucket
	now     func() time.Time
}

// memBucket holds the timestamps of allowed events for one key, oldest
// first. Cap (Burst) bounds the slice length so the per-key footprint is
// constant.
type memBucket struct {
	events []time.Time
}

// NewMemoryLimiter constructs a MemoryLimiter for the given Limit. A
// zero-Rate limit yields a limiter that returns Allowed=true without
// recording state.
func NewMemoryLimiter(limit Limit) *MemoryLimiter {
	return &MemoryLimiter{
		limit:   limit.Normalize(),
		buckets: make(map[string]*memBucket),
		now:     time.Now,
	}
}

// SetClock overrides the time source. Used in tests for deterministic
// window math.
func (m *MemoryLimiter) SetClock(c func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = c
}

// Allow charges n events against key's budget using the limiter's
// default Limit. See AllowWithLimit for the override-capable variant.
func (m *MemoryLimiter) Allow(ctx context.Context, key string, n int) (Decision, error) {
	return m.AllowWithLimit(ctx, key, n, m.limit)
}

// AllowWithLimit charges n events against key's budget using lim. When
// lim is unlimited (Rate == 0) the call returns Allowed=true without
// touching state. Callers that want to apply a per-token override should
// call this directly with the override's Limit.
//
// The same bucket store is shared regardless of lim; the effective
// ceiling is whichever Limit the caller passes. This matches the redis
// limiter's behaviour, where the Lua script accepts max_count from ARGV
// on every call.
func (m *MemoryLimiter) AllowWithLimit(ctx context.Context, key string, n int, lim Limit) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	lim = lim.Normalize()
	if lim.Unlimited() {
		return AllowDecisionUnlimited, nil
	}
	if n <= 0 {
		n = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	cutoff := now.Add(-lim.Per)
	b, ok := m.buckets[key]
	if !ok {
		b = &memBucket{events: make([]time.Time, 0, lim.Burst)}
		m.buckets[key] = b
	}
	// Drop entries older than the window.
	drop := 0
	for drop < len(b.events) && b.events[drop].Before(cutoff) {
		drop++
	}
	if drop > 0 {
		b.events = b.events[drop:]
	}
	ceiling := lim.Burst
	if ceiling < lim.Rate {
		ceiling = lim.Rate
	}
	if len(b.events)+n > ceiling {
		var reset time.Duration
		if len(b.events) > 0 {
			reset = b.events[0].Add(lim.Per).Sub(now)
			if reset < 0 {
				reset = 0
			}
		}
		return Decision{Allowed: false, Remaining: 0, Reset: reset}, nil
	}
	for i := 0; i < n; i++ {
		b.events = append(b.events, now)
	}
	remaining := ceiling - len(b.events)
	if remaining < 0 {
		remaining = 0
	}
	var reset time.Duration
	if len(b.events) > 0 {
		reset = b.events[0].Add(lim.Per).Sub(now)
		if reset < 0 {
			reset = 0
		}
	}
	return Decision{Allowed: true, Remaining: remaining, Reset: reset}, nil
}

// Sweep removes empty bucket entries. Safe to call from a background
// goroutine. Returns the number of entries removed.
func (m *MemoryLimiter) Sweep() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	cutoff := now.Add(-m.limit.Per)
	removed := 0
	for k, b := range m.buckets {
		// Drop expired events.
		drop := 0
		for drop < len(b.events) && b.events[drop].Before(cutoff) {
			drop++
		}
		if drop > 0 {
			b.events = b.events[drop:]
		}
		if len(b.events) == 0 {
			delete(m.buckets, k)
			removed++
		}
	}
	return removed
}

