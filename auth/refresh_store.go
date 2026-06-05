package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// RefreshStore tracks refresh-token rotation state. Two records per
// rotation chain:
//
//   - JTI redemption: each refresh's unique JTI is marked redeemed on
//     successful exchange. Re-presenting the same JTI must be treated
//     as a credential leak — the typical operator response is to
//     revoke the family.
//   - Family revocation: the FID shared by every refresh in a chain.
//     A revoked family rejects every descendant and sibling refresh
//     for the remaining lifetime of the chain.
//
// Implementations MUST be safe for concurrent use. Storage backends:
// MemoryRefreshStore (single-node, in-process) and RedisRefreshStore
// (multi-node, shared TTL via Redis EXPIRE).
//
// All methods accept an absolute exp time so the store can self-prune
// (memory) or set the right TTL (Redis). Records before time.Now() are
// no-ops — the predecessor refresh has already aged out and there is
// no value left to protect.
type RefreshStore interface {
	// MarkRedeemed records jti as redeemed. Returns ErrRefreshReused
	// if jti was already marked, leaving the store unchanged.
	MarkRedeemed(ctx context.Context, jti string, exp time.Time) error
	// RevokeFamily marks fid as revoked until exp. Idempotent. Used
	// when reuse detection trips on any JTI in the chain.
	RevokeFamily(ctx context.Context, fid string, exp time.Time) error
	// IsFamilyRevoked reports whether fid is currently revoked. Used
	// on every refresh redemption before MarkRedeemed.
	IsFamilyRevoked(ctx context.Context, fid string) (bool, error)
}

// Sentinel errors returned by RefreshStore implementations.
var (
	// ErrRefreshReused signals that a refresh JTI was presented for
	// redemption after it had already been redeemed. The caller MUST
	// follow up with RevokeFamily on the FID and refuse the request.
	ErrRefreshReused = errors.New("auth: refresh token already redeemed")
	// ErrFamilyRevoked signals that the refresh's family was already
	// marked revoked. The caller refuses the request.
	ErrFamilyRevoked = errors.New("auth: refresh family revoked")
)

// MemoryRefreshStore is the single-node RefreshStore. Records are held
// in maps; a periodic pruner clears entries past their exp. The zero
// value is unusable — construct via NewMemoryRefreshStore.
type MemoryRefreshStore struct {
	mu       sync.Mutex
	redeemed map[string]time.Time
	revoked  map[string]time.Time
	clock    func() time.Time
	stop     chan struct{}
	stopped  bool
}

// NewMemoryRefreshStore constructs a MemoryRefreshStore with a
// background pruner that wakes every interval. interval <= 0 disables
// background pruning (entries are still cleaned lazily on every
// read/write path).
func NewMemoryRefreshStore(interval time.Duration) *MemoryRefreshStore {
	s := &MemoryRefreshStore{
		redeemed: map[string]time.Time{},
		revoked:  map[string]time.Time{},
		clock:    time.Now,
		stop:     make(chan struct{}),
	}
	if interval > 0 {
		go s.runPruner(interval)
	}
	return s
}

// MarkRedeemed implements RefreshStore.
func (s *MemoryRefreshStore) MarkRedeemed(_ context.Context, jti string, exp time.Time) error {
	now := s.clock()
	if !exp.After(now) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if existing, ok := s.redeemed[jti]; ok && existing.After(now) {
		return ErrRefreshReused
	}
	s.redeemed[jti] = exp
	return nil
}

// RevokeFamily implements RefreshStore.
func (s *MemoryRefreshStore) RevokeFamily(_ context.Context, fid string, exp time.Time) error {
	now := s.clock()
	if !exp.After(now) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if existing, ok := s.revoked[fid]; !ok || exp.After(existing) {
		s.revoked[fid] = exp
	}
	return nil
}

// IsFamilyRevoked implements RefreshStore.
func (s *MemoryRefreshStore) IsFamilyRevoked(_ context.Context, fid string) (bool, error) {
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	exp, ok := s.revoked[fid]
	if !ok {
		return false, nil
	}
	return exp.After(now), nil
}

// Close stops the pruner. Safe to call multiple times.
func (s *MemoryRefreshStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.stop)
}

// SetClock overrides the time source. Used in tests.
func (s *MemoryRefreshStore) SetClock(c func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = c
}

// pruneLocked drops every record past its exp. Caller holds s.mu.
func (s *MemoryRefreshStore) pruneLocked(now time.Time) {
	for k, exp := range s.redeemed {
		if !exp.After(now) {
			delete(s.redeemed, k)
		}
	}
	for k, exp := range s.revoked {
		if !exp.After(now) {
			delete(s.revoked, k)
		}
	}
}

func (s *MemoryRefreshStore) runPruner(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.mu.Lock()
			s.pruneLocked(s.clock())
			s.mu.Unlock()
		}
	}
}
