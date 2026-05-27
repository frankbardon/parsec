package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

// RedisKeyRingStore persists the KeyRing as a JSON blob in Redis with a
// monotonic version counter. Save uses WATCH/MULTI to detect concurrent
// writes; Watch listens on a pub/sub channel for cross-node fanout of
// rotation events.
type RedisKeyRingStore struct {
	client    redis.UniversalClient
	keyPrefix string
}

// NewRedisKeyRingStore constructs a store backed by client. The default
// key prefix is "parsec"; the on-disk layout becomes:
//
//	<prefix>:keyring          string (JSON snapshot)
//	<prefix>:keyring:version  integer (monotonic)
//	<prefix>:keyring:events   pub/sub channel (version-number payloads)
func NewRedisKeyRingStore(client redis.UniversalClient) *RedisKeyRingStore {
	return &RedisKeyRingStore{client: client, keyPrefix: "parsec"}
}

// WithKeyPrefix overrides the namespace.
func (s *RedisKeyRingStore) WithKeyPrefix(p string) *RedisKeyRingStore {
	if p == "" {
		p = "parsec"
	}
	s.keyPrefix = p
	return s
}

func (s *RedisKeyRingStore) keyringKey() string { return s.keyPrefix + ":keyring" }
func (s *RedisKeyRingStore) versionKey() string { return s.keyPrefix + ":keyring:version" }
func (s *RedisKeyRingStore) eventChan() string  { return s.keyPrefix + ":keyring:events" }

// Load reads the persisted snapshot. Returns (nil, os.ErrNotExist) when
// the key does not exist so the caller can bootstrap.
func (s *RedisKeyRingStore) Load(ctx context.Context) (*KeyRing, error) {
	body, err := s.client.Get(ctx, s.keyringKey()).Result()
	if errors.Is(err, redis.Nil) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("redis Get keyring: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		return nil, fmt.Errorf("decode keyring: %w", err)
	}
	if snap.FormatVersion != "" && snap.FormatVersion != "1" {
		return nil, fmt.Errorf("keyring format_version %q is not supported", snap.FormatVersion)
	}
	r := NewKeyRing()
	if err := r.LoadSnapshot(snap); err != nil {
		return nil, err
	}
	return r, nil
}

// Save persists ring with optimistic concurrency. Uses WATCH on the
// version key so concurrent writes from two nodes are detected and the
// loser must reload + retry. Bumps the version and publishes it on the
// pub/sub channel so other nodes can react.
func (s *RedisKeyRingStore) Save(ctx context.Context, r *KeyRing) error {
	snap := r.Snapshot()
	snap.FormatVersion = "1"
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	for range 5 {
		err = s.client.Watch(ctx, func(tx *redis.Tx) error {
			version, verr := tx.Get(ctx, s.versionKey()).Int64()
			if verr != nil && !errors.Is(verr, redis.Nil) {
				return verr
			}
			_, perr := tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				p.Set(ctx, s.keyringKey(), body, 0)
				p.Set(ctx, s.versionKey(), version+1, 0)
				p.Publish(ctx, s.eventChan(), version+1)
				return nil
			})
			return perr
		}, s.versionKey())
		if err == nil {
			return nil
		}
		if !errors.Is(err, redis.TxFailedErr) {
			return fmt.Errorf("save keyring: %w", err)
		}
		// optimistic-concurrency retry
	}
	return fmt.Errorf("save keyring: contention after retries")
}

// Watch subscribes to the events channel and re-loads + fires onChange on
// every version bump it sees from another node. Own writes are observable
// too — callers may dedupe by comparing the resulting snapshot. Blocks
// until ctx is canceled.
func (s *RedisKeyRingStore) Watch(ctx context.Context, onChange func(*KeyRing)) error {
	pubsub := s.client.Subscribe(ctx, s.eventChan())
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		return err
	}
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			r, err := s.Load(ctx)
			if err != nil {
				continue
			}
			onChange(r)
		}
	}
}

// Ensure returns a ring, bootstrapping if empty. Mirrors the file store
// bootstrap pattern; the resulting ring is persisted on bootstrap.
func (s *RedisKeyRingStore) Ensure(ctx context.Context) (*KeyRing, bool, error) {
	r, err := s.Load(ctx)
	if err == nil {
		return r, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	r = NewKeyRing()
	if _, err := r.Generate(); err != nil {
		return nil, false, err
	}
	if err := s.Save(ctx, r); err != nil {
		return nil, false, err
	}
	return r, true, nil
}
