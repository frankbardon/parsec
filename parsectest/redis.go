package parsectest

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// NewWithRedis returns an *Instance backed by an in-process miniredis.
// The miniredis instance is started, attached to parsec as both the
// go-redis client AND the broker RedisAddr (so the centrifuge Redis
// broker swap kicks in), and torn down on t.Cleanup.
//
// Use this when a test needs to exercise the multi-node code paths
// (Redis broker, Redis channel registry, Redis-watched keyring, Redis
// DLQ, Redis rate limiter) without a docker container.
//
// Note: miniredis implements the Redis wire protocol in Go but is not
// byte-identical to real Redis on every command. The vast majority of
// parsec code paths are covered; if a specific Lua script under test
// hits an edge miniredis does not emulate, point WithRedis at a real
// container instead.
func NewWithRedis(t testing.TB, opts ...Option) *Instance {
	t.Helper()
	return newWithRedisServer(t, false, opts...)
}

// NewServerWithRedis is NewWithRedis composed with NewServer — the
// returned Instance has both the *httptest.Server and the miniredis
// backing.
func NewServerWithRedis(t testing.TB, opts ...Option) *Instance {
	t.Helper()
	return newWithRedisServer(t, true, opts...)
}

func newWithRedisServer(t testing.TB, withServer bool, opts ...Option) *Instance {
	t.Helper()
	mr := miniredis.RunT(t) // RunT registers Close on t.Cleanup itself

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	prepend := []Option{
		func(c *config) {
			c.pOpts.RedisClient = client
			c.pOpts.RedisAddr = mr.Addr()
		},
	}
	all := append(prepend, opts...)

	if withServer {
		return NewServer(t, all...)
	}
	return New(t, all...)
}
