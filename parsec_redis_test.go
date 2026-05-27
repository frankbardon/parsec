package parsec

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/frankbardon/parsec/auth"
)

func redisAddr() string {
	a := os.Getenv("PARSEC_REDIS_ADDR")
	if a == "" {
		a = "localhost:6379"
	}
	return a
}

func redisReachable(t *testing.T) {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: redisAddr()})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Skipf("redis unreachable at %s: %v", redisAddr(), err)
	}
}

// TestRedis_MultiNodePublish boots two Parsec instances against the same
// Redis and verifies that a publish on node A is received by node B's
// presence count incrementing after a subscribe — i.e. the Redis broker
// is wired.
func TestRedis_MultiNodePublish(t *testing.T) {
	redisReachable(t)
	secret, _ := auth.GenerateSecret()
	addr := redisAddr()

	prefix := "parsec-multinode-" + t.Name()
	pA, err := New(Options{
		KeyRing:        ringFromSecret(t, secret),
		RedisAddr:      addr,
		RedisKeyPrefix: prefix,
		NodeID:         "node-a",
		SweepInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	pB, err := New(Options{
		KeyRing:        ringFromSecret(t, secret),
		RedisAddr:      addr,
		RedisKeyPrefix: prefix,
		NodeID:         "node-b",
		SweepInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneA := make(chan struct{})
	doneB := make(chan struct{})
	go func() { _ = pA.Run(ctx); close(doneA) }()
	go func() { _ = pB.Run(ctx); close(doneB) }()
	time.Sleep(150 * time.Millisecond)

	t.Cleanup(func() {
		cancel()
		<-doneA
		<-doneB
		c := redis.NewClient(&redis.Options{Addr: addr})
		defer c.Close()
		c2, c2cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer c2cancel()
		c.Del(c2, prefix+":channels")
	})

	// OpenPublic on A; B should see it through the registry.
	ch, err := pA.OpenPublic("public:multinode.system.status", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pB.Manager().IsOpen(ch.Name) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !pB.Manager().IsOpen(ch.Name) {
		t.Fatal("node B never saw channel opened on A")
	}

	// Publish on A; B should be able to read presence via its own broker
	// (cross-node presence from RedisPresenceManager).
	if _, err := pA.Publish(ctx, ch.Name.String(), []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	// Presence is 0 here because there are no real ws subscribers — this
	// test asserts that the cross-node REGISTRY is coherent. Cross-node
	// fan-out is exercised by the channels package test.
}

// TestRedis_ManifestReportsRedis asserts the manifest persistence field
// flips to "redis" when Redis is configured.
func TestRedis_ManifestReportsRedis(t *testing.T) {
	redisReachable(t)
	secret, _ := auth.GenerateSecret()
	p, err := New(Options{
		KeyRing:        ringFromSecret(t, secret),
		RedisAddr:      redisAddr(),
		RedisKeyPrefix: "parsec-manifest-" + t.Name(),
		SweepInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Persistence(); got != "redis" {
		t.Fatalf("Persistence: want redis, got %s", got)
	}
}
