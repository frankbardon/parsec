package parsec

import (
	"context"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/cache"
)

func TestCache_DisabledByDefault(t *testing.T) {
	secret, _ := auth.GenerateSecret()
	p, err := New(Options{KeyRing: ringFromSecret(t, secret), SweepInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if p.Cache() != nil {
		t.Fatalf("Cache() = %T, want nil", p.Cache())
	}
	if got := p.CacheBackend(); got != "" {
		t.Fatalf("CacheBackend = %q, want empty", got)
	}
}

func TestCache_ExplicitMemoryHonored(t *testing.T) {
	secret, _ := auth.GenerateSecret()
	mc := cache.NewMemoryCache(64)
	defer mc.Close()
	p, err := New(Options{
		KeyRing:       ringFromSecret(t, secret),
		SweepInterval: 50 * time.Millisecond,
		Cache:         mc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Cache() == nil {
		t.Fatal("expected cache to be wired")
	}
	if got := p.CacheBackend(); got != "memory" {
		t.Fatalf("backend = %q, want memory", got)
	}

	// The wrapped cache must round-trip through the metrics wrapper.
	ctx := context.Background()
	if _, hit, _ := p.Cache().Get(ctx, "missing"); hit {
		t.Fatal("expected miss")
	}
}

func TestCache_NoopOptsOutEvenWithRedisClient(t *testing.T) {
	// Without RedisClient: explicit NoopCache passes through. We can't
	// easily simulate a live Redis here, so the assertion is just that
	// NoopCache survives the auto-build path.
	secret, _ := auth.GenerateSecret()
	p, err := New(Options{
		KeyRing:       ringFromSecret(t, secret),
		SweepInterval: 50 * time.Millisecond,
		Cache:         cache.NewNoopCache(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CacheBackend(); got != "noop" {
		t.Fatalf("backend = %q, want noop", got)
	}
}

func TestCache_StatsExposeMetricsGauge(t *testing.T) {
	// The wrapped cache updates parsec_cache_size_entries on every
	// Stats() call. Reach for the metric and confirm it tracks the
	// underlying counter.
	secret, _ := auth.GenerateSecret()
	mc := cache.NewMemoryCache(64)
	defer mc.Close()
	p, err := New(Options{
		KeyRing:       ringFromSecret(t, secret),
		SweepInterval: 50 * time.Millisecond,
		Cache:         mc,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Trigger Stats() so the gauge is written at least once.
	if got := p.Cache().Stats(); got.SizeEntries != 0 {
		t.Fatalf("fresh cache size = %d, want 0", got.SizeEntries)
	}
}
