package channels

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	perr "github.com/frankbardon/parsec/errors"
)

func redisAddrFromEnv() string {
	addr := os.Getenv("PARSEC_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	return addr
}

func newTestRedisClient(t *testing.T) redis.UniversalClient {
	t.Helper()
	addr := redisAddrFromEnv()
	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	return client
}

func freshRedisStore(t *testing.T) (*RedisStore, redis.UniversalClient) {
	t.Helper()
	client := newTestRedisClient(t)
	// Each test uses its own key prefix to avoid cross-test contamination.
	prefix := "parsec-test-" + t.Name()
	store := NewRedisStore(client).WithKeyPrefix(prefix)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		client.Del(ctx, store.hashKey())
		_ = client.Close()
	})
	return store, client
}

func TestRedisStore_CreatePrivateAndGet(t *testing.T) {
	store, _ := freshRedisStore(t)
	now := time.Now()
	n, _ := ParseName("private:webapp.user.42.notifications")

	ch, ev, err := store.CreatePrivate(n, 5*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != EventOpened {
		t.Fatalf("expected EventOpened, got %s", ev.Kind)
	}
	if ch.State != StateOpen {
		t.Fatalf("expected open state, got %s", ch.State)
	}

	got, err := store.Get(n)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name.String() != n.String() {
		t.Fatalf("name mismatch: %s", got.Name.String())
	}
}

func TestRedisStore_CreatePrivateDuplicate(t *testing.T) {
	store, _ := freshRedisStore(t)
	n, _ := ParseName("private:test.user.7.notif")
	now := time.Now()
	if _, _, err := store.CreatePrivate(n, time.Minute, now); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.CreatePrivate(n, time.Minute, now)
	if err == nil {
		t.Fatal("expected duplicate rejection")
	}
	var pe *perr.Error
	if !errors.As(err, &pe) || pe.Code != perr.ChannelExists {
		t.Fatalf("expected ChannelExists, got %v", err)
	}
}

func TestRedisStore_OpenPublicAndReopen(t *testing.T) {
	store, _ := freshRedisStore(t)
	n, _ := ParseName("public:webapp.system.status")
	now := time.Now()

	ch, ev, err := store.OpenPublic(n, 50*time.Millisecond, now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != EventOpened {
		t.Fatalf("expected opened, got %s", ev.Kind)
	}
	_ = ch

	// Sweep into closed.
	events, err := store.Sweep(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != EventClosed {
		t.Fatalf("expected 1 closed event, got %+v", events)
	}

	// Re-open.
	_, ev, err = store.OpenPublic(n, time.Minute, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != EventOpened {
		t.Fatalf("expected reopened, got %s", ev.Kind)
	}
}

func TestRedisStore_DeleteIdempotent(t *testing.T) {
	store, _ := freshRedisStore(t)
	n, _ := ParseName("public:test.system.status")
	now := time.Now()
	_, _, _ = store.OpenPublic(n, time.Minute, now)

	ev, err := store.Delete(n, now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != EventDeleted {
		t.Fatalf("expected deleted, got %s", ev.Kind)
	}
	// Idempotent — second call emits nothing.
	ev, err = store.Delete(n, now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != "" {
		t.Fatalf("expected empty event on redundant delete, got %s", ev.Kind)
	}
}

func TestRedisStore_TouchRefreshesLastActive(t *testing.T) {
	store, _ := freshRedisStore(t)
	n, _ := ParseName("public:test.system.status")
	now := time.Now()
	_, _, _ = store.OpenPublic(n, time.Minute, now)

	later := now.Add(30 * time.Second)
	if err := store.Touch(n, later); err != nil {
		t.Fatal(err)
	}
	ch, err := store.Get(n)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.LastActive.Equal(later) {
		t.Fatalf("LastActive not refreshed: want %v got %v", later, ch.LastActive)
	}
}

func TestRedisStore_SweepPrivateDeletes(t *testing.T) {
	store, _ := freshRedisStore(t)
	n, _ := ParseName("private:test.session.x.notif")
	now := time.Now()
	_, _, _ = store.CreatePrivate(n, 50*time.Millisecond, now)

	events, err := store.Sweep(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != EventDeleted {
		t.Fatalf("expected 1 deleted event, got %+v", events)
	}
	if _, err := store.Get(n); err == nil {
		t.Fatal("expected ChannelNotFound after sweep")
	}
}

func TestRedisStore_TwoNodesShareRegistry(t *testing.T) {
	clientA := newTestRedisClient(t)
	prefix := "parsec-share-" + t.Name()
	storeA := NewRedisStore(clientA).WithKeyPrefix(prefix)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		clientA.Del(ctx, storeA.hashKey())
		_ = clientA.Close()
	})

	clientB := redis.NewClient(&redis.Options{Addr: redisAddrFromEnv()})
	defer clientB.Close()
	storeB := NewRedisStore(clientB).WithKeyPrefix(prefix)

	n, _ := ParseName("public:shared.system.status")
	now := time.Now()
	if _, _, err := storeA.OpenPublic(n, time.Minute, now); err != nil {
		t.Fatal(err)
	}
	got, err := storeB.Get(n)
	if err != nil {
		t.Fatal("node B should see node A's channel: ", err)
	}
	if got.State != StateOpen {
		t.Fatalf("expected open on B, got %s", got.State)
	}
}
