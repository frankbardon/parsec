package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRefreshStore implements RefreshStore against Redis using SETNX
// for redemption atomicity and an EX TTL matched to the refresh's own
// expiry so storage self-prunes. Keyspace (with default prefix
// "parsec"):
//
//	<prefix>:refresh:jti:<jti>  "1"  (TTL = exp - now)
//	<prefix>:refresh:fid:<fid>  "1"  (TTL = exp - now)
//
// Both keys are flag-style — their presence is the signal; the value
// is a placeholder.
type RedisRefreshStore struct {
	client    redis.UniversalClient
	keyPrefix string
	clock     func() time.Time
}

// NewRedisRefreshStore constructs a store backed by client.
func NewRedisRefreshStore(client redis.UniversalClient) *RedisRefreshStore {
	return &RedisRefreshStore{client: client, keyPrefix: "parsec", clock: time.Now}
}

// WithKeyPrefix overrides the namespace. Empty resets to "parsec".
func (s *RedisRefreshStore) WithKeyPrefix(p string) *RedisRefreshStore {
	if p == "" {
		p = "parsec"
	}
	s.keyPrefix = p
	return s
}

// SetClock overrides the time source. Used in tests.
func (s *RedisRefreshStore) SetClock(c func() time.Time) { s.clock = c }

func (s *RedisRefreshStore) jtiKey(jti string) string { return s.keyPrefix + ":refresh:jti:" + jti }
func (s *RedisRefreshStore) fidKey(fid string) string { return s.keyPrefix + ":refresh:fid:" + fid }

// MarkRedeemed implements RefreshStore. SETNX returns false when the
// key already exists, which the caller surfaces as ErrRefreshReused.
func (s *RedisRefreshStore) MarkRedeemed(ctx context.Context, jti string, exp time.Time) error {
	ttl := exp.Sub(s.clock())
	if ttl <= 0 {
		return nil
	}
	ok, err := s.client.SetNX(ctx, s.jtiKey(jti), "1", ttl).Result()
	if err != nil {
		return fmt.Errorf("redis SETNX jti: %w", err)
	}
	if !ok {
		return ErrRefreshReused
	}
	return nil
}

// RevokeFamily implements RefreshStore. Set is idempotent; we use a
// straight SET with EX so a longer remaining TTL replaces a shorter
// one if a later revocation extends the window.
func (s *RedisRefreshStore) RevokeFamily(ctx context.Context, fid string, exp time.Time) error {
	ttl := exp.Sub(s.clock())
	if ttl <= 0 {
		return nil
	}
	if err := s.client.Set(ctx, s.fidKey(fid), "1", ttl).Err(); err != nil {
		return fmt.Errorf("redis SET fid: %w", err)
	}
	return nil
}

// IsFamilyRevoked implements RefreshStore.
func (s *RedisRefreshStore) IsFamilyRevoked(ctx context.Context, fid string) (bool, error) {
	n, err := s.client.Exists(ctx, s.fidKey(fid)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("redis EXISTS fid: %w", err)
	}
	return n > 0, nil
}
