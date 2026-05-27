package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newRedisClient(t *testing.T) redis.UniversalClient {
	t.Helper()
	addr := os.Getenv("PARSEC_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Skipf("redis unreachable at %s: %v", addr, err)
	}
	return c
}

func freshRedisKeyRingStore(t *testing.T) *RedisKeyRingStore {
	t.Helper()
	client := newRedisClient(t)
	prefix := "parsec-keyring-" + t.Name()
	s := NewRedisKeyRingStore(client).WithKeyPrefix(prefix)
	// Wipe any leftover state.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	client.Del(ctx, s.keyringKey(), s.versionKey())
	cancel()
	t.Cleanup(func() {
		c, ccancel := context.WithTimeout(context.Background(), time.Second)
		defer ccancel()
		client.Del(c, s.keyringKey(), s.versionKey())
		_ = client.Close()
	})
	return s
}

func TestRedisKeyRingStore_EnsureBootstrap(t *testing.T) {
	s := freshRedisKeyRingStore(t)
	ctx := t.Context()
	r, bootstrapped, err := s.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap=true on empty store")
	}
	if r.ActiveID() == "" {
		t.Fatal("expected an active key after bootstrap")
	}

	// Second Ensure must return the SAME ring (no bootstrap).
	r2, bootstrapped2, err := s.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrapped2 {
		t.Fatal("expected bootstrap=false on second Ensure")
	}
	if r2.ActiveID() != r.ActiveID() {
		t.Fatalf("active id changed across Ensure calls: %s vs %s", r.ActiveID(), r2.ActiveID())
	}
}

func TestRedisKeyRingStore_SaveLoadRoundTrip(t *testing.T) {
	s := freshRedisKeyRingStore(t)
	ctx := t.Context()
	r := NewKeyRing()
	if _, err := r.Generate(); err != nil {
		t.Fatal(err)
	}
	originalActive := r.ActiveID()
	if err := s.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveID() != originalActive {
		t.Fatalf("active mismatch after round-trip: want %s got %s", originalActive, got.ActiveID())
	}
}

func TestRedisKeyRingStore_TwoNodesRotate(t *testing.T) {
	// Two stores against the same backing key prefix simulate two Parsec nodes.
	clientA := newRedisClient(t)
	prefix := "parsec-rotate-" + t.Name()
	storeA := NewRedisKeyRingStore(clientA).WithKeyPrefix(prefix)
	clientB := newRedisClient(t)
	storeB := NewRedisKeyRingStore(clientB).WithKeyPrefix(prefix)
	// Wipe + cleanup
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	clientA.Del(ctx, storeA.keyringKey(), storeA.versionKey())
	cancel()
	t.Cleanup(func() {
		c, ccancel := context.WithTimeout(context.Background(), time.Second)
		defer ccancel()
		clientA.Del(c, storeA.keyringKey(), storeA.versionKey())
		_ = clientA.Close()
		_ = clientB.Close()
	})

	wctx := t.Context()
	ringA, _, err := storeA.Ensure(wctx)
	if err != nil {
		t.Fatal(err)
	}

	// Generate a new key on node A; promote it.
	newKey, err := ringA.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := ringA.Promote(newKey.ID); err != nil {
		t.Fatal(err)
	}
	if err := storeA.Save(wctx, ringA); err != nil {
		t.Fatal(err)
	}

	// Node B loads — must see the new active.
	ringB, err := storeB.Load(wctx)
	if err != nil {
		t.Fatal(err)
	}
	if ringB.ActiveID() != newKey.ID {
		t.Fatalf("node B did not see promoted key: want %s got %s", newKey.ID, ringB.ActiveID())
	}
}

func TestRedisKeyRingStore_WatchFiresOnRemoteSave(t *testing.T) {
	clientA := newRedisClient(t)
	prefix := "parsec-watch-" + t.Name()
	storeA := NewRedisKeyRingStore(clientA).WithKeyPrefix(prefix)
	clientB := newRedisClient(t)
	storeB := NewRedisKeyRingStore(clientB).WithKeyPrefix(prefix)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	clientA.Del(ctx, storeA.keyringKey(), storeA.versionKey())
	cancel()
	t.Cleanup(func() {
		c, ccancel := context.WithTimeout(context.Background(), time.Second)
		defer ccancel()
		clientA.Del(c, storeA.keyringKey(), storeA.versionKey())
		_ = clientA.Close()
		_ = clientB.Close()
	})

	wctx, wcancel := context.WithCancel(t.Context())
	defer wcancel()

	got := make(chan *KeyRing, 4)
	go func() { _ = storeB.Watch(wctx, func(r *KeyRing) { got <- r }) }()
	time.Sleep(100 * time.Millisecond) // let SUBSCRIBE land

	r := NewKeyRing()
	if _, err := r.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := storeA.Save(wctx, r); err != nil {
		t.Fatal(err)
	}
	select {
	case ring := <-got:
		if ring.ActiveID() != r.ActiveID() {
			t.Fatalf("watch delivered wrong active: want %s got %s", r.ActiveID(), ring.ActiveID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch never fired on remote save")
	}
}
