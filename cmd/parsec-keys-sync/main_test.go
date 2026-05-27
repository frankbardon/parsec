package main

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisAvailable probes localhost:6379. The sync test depends on two
// real Redis databases (we use logical DB 0 and DB 1 on the same
// instance). When Redis is unreachable we skip — the daemon's logic
// is pure pub/sub bridging and a unit-level mock would not cover
// anything the integration test doesn't.
func redisAvailable(t *testing.T) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// TestRun_ForwardsPubSubBetweenRegions stands up two go-redis clients
// (different logical DBs to simulate distinct regions on a single
// Redis instance — see redisAvailable's note) and asserts that a
// publish on the source's <prefix>:keyring:events channel is
// observed verbatim on every target.
func TestRun_ForwardsPubSubBetweenRegions(t *testing.T) {
	if !redisAvailable(t) {
		t.Skip("redis unreachable at localhost:6379")
	}
	const prefix = "keysynctest"
	src := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 0})
	defer src.Close()
	tgt := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 1})
	defer tgt.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Subscribe on the target so we can observe the forwarded payload.
	tgtSub := tgt.Subscribe(ctx, channelName(prefix))
	defer tgtSub.Close()
	if _, err := tgtSub.Receive(ctx); err != nil {
		t.Fatalf("target subscribe: %v", err)
	}
	tgtCh := tgtSub.Channel()

	// Start the bridge.
	bridgeCtx, bridgeCancel := context.WithCancel(ctx)
	defer bridgeCancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(bridgeCtx, slog.New(slog.DiscardHandler), src,
			[]*targetClient{{URL: "tgt", Client: tgt}}, prefix)
	}()
	// Give the bridge a moment to subscribe — Subscribe.Receive only
	// returns *after* the channel handshake completes, but the bridge
	// is on its own goroutine so we wait briefly to avoid racing the
	// publish.
	time.Sleep(150 * time.Millisecond)

	// Publish on the source. payload is the version-number string
	// Parsec's RedisKeyRingStore uses.
	if err := src.Publish(ctx, channelName(prefix), "42").Err(); err != nil {
		t.Fatalf("source publish: %v", err)
	}

	select {
	case msg := <-tgtCh:
		if msg.Payload != "42" {
			t.Errorf("forwarded payload = %q, want %q", msg.Payload, "42")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe forwarded message within 3s")
	}

	// Shutdown is graceful: cancel the bridge ctx, expect a clean
	// return.
	bridgeCancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Errorf("Run returned non-canceled error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit within 3s of cancel")
	}
}

// TestStripRedisScheme exercises the URL-normalizer in isolation —
// pure function, fast unit case.
func TestStripRedisScheme(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"127.0.0.1:6379", "127.0.0.1:6379"},
		{"redis://127.0.0.1:6379", "127.0.0.1:6379"},
		{"rediss://example.com:6380", "example.com:6380"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := stripRedisScheme(tc.in); got != tc.want {
			t.Errorf("stripRedisScheme(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestChannelName confirms the constant Parsec's RedisKeyRingStore
// uses (<prefix>:keyring:events). If the daemon and the store ever
// drift on this constant the bridge silently does nothing — make the
// contract explicit.
func TestChannelName(t *testing.T) {
	if got := channelName("parsec"); got != "parsec:keyring:events" {
		t.Errorf("channelName = %q, want parsec:keyring:events", got)
	}
}
