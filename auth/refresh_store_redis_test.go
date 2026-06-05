package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newMiniRedisStore(t *testing.T) (*RedisRefreshStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisRefreshStore(client), mr
}

func TestRedisRefreshStoreMarkRedeemed(t *testing.T) {
	s, _ := newMiniRedisStore(t)
	ctx := t.Context()
	exp := time.Now().Add(time.Hour)

	if err := s.MarkRedeemed(ctx, "jti-a", exp); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if err := s.MarkRedeemed(ctx, "jti-a", exp); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("second redeem err=%v, want ErrRefreshReused", err)
	}
	if err := s.MarkRedeemed(ctx, "jti-b", exp); err != nil {
		t.Fatalf("redeem distinct jti: %v", err)
	}
}

func TestRedisRefreshStoreRevokeFamily(t *testing.T) {
	s, _ := newMiniRedisStore(t)
	ctx := t.Context()
	exp := time.Now().Add(time.Hour)

	revoked, err := s.IsFamilyRevoked(ctx, "fid")
	if err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Fatal("fresh fid reported revoked")
	}
	if err := s.RevokeFamily(ctx, "fid", exp); err != nil {
		t.Fatal(err)
	}
	revoked, err = s.IsFamilyRevoked(ctx, "fid")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("revoked fid reported not revoked")
	}
}

func TestRedisRefreshStoreKeyPrefix(t *testing.T) {
	s, mr := newMiniRedisStore(t)
	s.WithKeyPrefix("acme")
	ctx := t.Context()
	if err := s.MarkRedeemed(ctx, "jti", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !mr.Exists("acme:refresh:jti:jti") {
		t.Fatalf("expected key under prefix; have %v", mr.Keys())
	}
}

func TestRedisRefreshStoreTTLApplied(t *testing.T) {
	s, mr := newMiniRedisStore(t)
	ctx := t.Context()
	exp := time.Now().Add(2 * time.Minute)
	if err := s.MarkRedeemed(ctx, "jti", exp); err != nil {
		t.Fatal(err)
	}
	ttl := mr.TTL("parsec:refresh:jti:jti")
	if ttl <= 0 || ttl > 2*time.Minute {
		t.Fatalf("ttl=%v out of expected window", ttl)
	}
}
