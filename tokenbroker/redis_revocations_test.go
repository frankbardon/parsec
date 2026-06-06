package tokenbroker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func redisAddr() string {
	a := os.Getenv("PARSEC_REDIS_ADDR")
	if a == "" {
		a = "localhost:6379"
	}
	return a
}

func redisOrSkip(t *testing.T) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: redisAddr()})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		c.Close()
		t.Skipf("redis unreachable at %s: %v", redisAddr(), err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func uniquePrefix(t *testing.T) string {
	return "parsec-test-" + t.Name()
}

func TestRedisRevocationsTokenRoundTrip(t *testing.T) {
	c := redisOrSkip(t)
	s := NewRedisRevocations(c).WithKeyPrefix(uniquePrefix(t))
	ctx := context.Background()

	ok, err := s.IsRevoked(ctx, "missing-id")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected unknown token to be unrevoked")
	}

	if err := s.Revoke(ctx, "tok-1", "user-1", "compromised"); err != nil {
		t.Fatal(err)
	}
	ok, err = s.IsRevoked(ctx, "tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected tok-1 revoked")
	}

	t.Cleanup(func() {
		_ = c.Del(ctx, s.tokenKey("tok-1")).Err()
	})
}

func TestRedisRevocationsUserBlanket(t *testing.T) {
	c := redisOrSkip(t)
	s := NewRedisRevocations(c).WithKeyPrefix(uniquePrefix(t))
	ctx := context.Background()

	if err := s.RevokeAllForUser(ctx, "user-7"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Del(ctx, s.userKey("user-7")).Err()
	})

	// A token issued an hour before the revoke moment must be flagged.
	old := time.Now().Add(-time.Hour)
	rev, err := s.IsUserRevoked(ctx, "user-7", old)
	if err != nil {
		t.Fatal(err)
	}
	if !rev {
		t.Fatal("pre-revoke token should be flagged")
	}

	// A token issued an hour AFTER the revoke moment must pass.
	fresh := time.Now().Add(time.Hour)
	rev, err = s.IsUserRevoked(ctx, "user-7", fresh)
	if err != nil {
		t.Fatal(err)
	}
	if rev {
		t.Fatal("post-revoke token should not be flagged")
	}
}

func TestRedisRevocationsRejectsEmptyArgs(t *testing.T) {
	// No redis dependency — pure argument validation.
	s := NewRedisRevocations(redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"}))
	if err := s.Revoke(context.Background(), "", "u", ""); err == nil {
		t.Fatal("expected empty tokenID rejected")
	}
	if err := s.RevokeAllForUser(context.Background(), ""); err == nil {
		t.Fatal("expected empty userID rejected")
	}
}
