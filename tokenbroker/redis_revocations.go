package tokenbroker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRevocations implements RevocationStore against Redis using SET
// with EX so storage self-prunes when revoked tokens age out past their
// natural expiry. Keyspace (with default prefix "parsec"):
//
//	<prefix>:tb:revoked:<tokenID>     "<reason>"                   (TTL = MaxTTL)
//	<prefix>:tb:user_revoked_at:<uid> "<unix-nanos of revoke time>" (TTL = MaxTTL)
//
// MaxTTL caps how long a revocation entry stays observable in Redis.
// Operators set it to at least the longest mgmt-token TTL plus a safety
// margin (24h is the package default — matches the mgmt clamp ceiling
// in auth.Issuer). Once a revocation entry ages out, the underlying
// token has expired anyway and Redis releases the bytes.
type RedisRevocations struct {
	client    redis.UniversalClient
	keyPrefix string
	maxTTL    time.Duration
	clock     func() time.Time
}

// NewRedisRevocations constructs a store backed by client. MaxTTL
// defaults to 24h; override with WithMaxTTL.
func NewRedisRevocations(client redis.UniversalClient) *RedisRevocations {
	return &RedisRevocations{
		client:    client,
		keyPrefix: "parsec",
		maxTTL:    24 * time.Hour,
		clock:     time.Now,
	}
}

// WithKeyPrefix overrides the namespace. Empty resets to "parsec".
func (s *RedisRevocations) WithKeyPrefix(p string) *RedisRevocations {
	if p == "" {
		p = "parsec"
	}
	s.keyPrefix = p
	return s
}

// WithMaxTTL overrides the TTL applied to every revocation entry.
// A zero or negative value resets to the 24h default.
func (s *RedisRevocations) WithMaxTTL(d time.Duration) *RedisRevocations {
	if d <= 0 {
		d = 24 * time.Hour
	}
	s.maxTTL = d
	return s
}

// SetClock overrides the time source. Used in tests.
func (s *RedisRevocations) SetClock(c func() time.Time) { s.clock = c }

func (s *RedisRevocations) tokenKey(id string) string {
	return s.keyPrefix + ":tb:revoked:" + id
}

func (s *RedisRevocations) userKey(uid string) string {
	return s.keyPrefix + ":tb:user_revoked_at:" + uid
}

// Revoke implements RevocationStore.
func (s *RedisRevocations) Revoke(ctx context.Context, tokenID, userID, reason string) error {
	if tokenID == "" {
		return errors.New("tokenbroker: Revoke requires tokenID")
	}
	val := reason
	if val == "" {
		val = "revoked"
	}
	if err := s.client.Set(ctx, s.tokenKey(tokenID), val, s.maxTTL).Err(); err != nil {
		return fmt.Errorf("redis SET revoked: %w", err)
	}
	_ = userID
	return nil
}

// IsRevoked implements RevocationStore.
func (s *RedisRevocations) IsRevoked(ctx context.Context, tokenID string) (bool, error) {
	if tokenID == "" {
		return false, nil
	}
	n, err := s.client.Exists(ctx, s.tokenKey(tokenID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("redis EXISTS revoked: %w", err)
	}
	return n > 0, nil
}

// RevokeAllForUser implements RevocationStore. The value stored is the
// unix-nano timestamp of the revoke moment; IsUserRevoked compares this
// against the token's issuedAt to decide whether a given token predates
// the blanket revocation.
func (s *RedisRevocations) RevokeAllForUser(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("tokenbroker: RevokeAllForUser requires userID")
	}
	now := s.clock().UTC()
	val := strconv.FormatInt(now.UnixNano(), 10)
	if err := s.client.Set(ctx, s.userKey(userID), val, s.maxTTL).Err(); err != nil {
		return fmt.Errorf("redis SET user_revoked_at: %w", err)
	}
	return nil
}

// IsUserRevoked implements RevocationStore.
func (s *RedisRevocations) IsUserRevoked(ctx context.Context, userID string, issuedAt time.Time) (bool, error) {
	if userID == "" {
		return false, nil
	}
	raw, err := s.client.Get(ctx, s.userKey(userID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("redis GET user_revoked_at: %w", err)
	}
	cutoffNanos, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		// Stale or malformed entry — fail closed: assume revoked so a
		// poisoned cache cannot silently re-authorize.
		return true, nil
	}
	cutoff := time.Unix(0, cutoffNanos).UTC()
	// "issuedAt <= cutoff" = pre-revoke token, revoked.
	return !issuedAt.After(cutoff), nil
}
