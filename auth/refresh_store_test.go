package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryRefreshStoreMarkRedeemed(t *testing.T) {
	s := NewMemoryRefreshStore(0)
	defer s.Close()
	ctx := t.Context()
	exp := time.Now().Add(time.Hour)

	if err := s.MarkRedeemed(ctx, "jti-1", exp); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if err := s.MarkRedeemed(ctx, "jti-1", exp); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("second redeem err=%v, want ErrRefreshReused", err)
	}
}

func TestMemoryRefreshStoreExpiredEntryAllowsReuse(t *testing.T) {
	s := NewMemoryRefreshStore(0)
	defer s.Close()
	ctx := t.Context()

	now := time.Now()
	s.SetClock(func() time.Time { return now })

	if err := s.MarkRedeemed(ctx, "jti", now.Add(time.Minute)); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	// Jump past expiry. The next redeem should succeed: the prior
	// record has aged out, the token is free to be issued again.
	now = now.Add(2 * time.Minute)
	if err := s.MarkRedeemed(ctx, "jti", now.Add(time.Minute)); err != nil {
		t.Fatalf("post-expiry redeem: %v", err)
	}
}

func TestMemoryRefreshStorePastExpIsNoop(t *testing.T) {
	s := NewMemoryRefreshStore(0)
	defer s.Close()
	ctx := t.Context()
	past := time.Now().Add(-time.Hour)
	if err := s.MarkRedeemed(ctx, "jti", past); err != nil {
		t.Fatalf("past exp: %v", err)
	}
	// Nothing should have been recorded, so a fresh redeem succeeds.
	if err := s.MarkRedeemed(ctx, "jti", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("redeem after no-op: %v", err)
	}
}

func TestMemoryRefreshStoreRevokeFamily(t *testing.T) {
	s := NewMemoryRefreshStore(0)
	defer s.Close()
	ctx := t.Context()
	exp := time.Now().Add(time.Hour)

	revoked, err := s.IsFamilyRevoked(ctx, "fid")
	if err != nil {
		t.Fatalf("pre-check: %v", err)
	}
	if revoked {
		t.Fatal("fresh store reports family revoked")
	}

	if err := s.RevokeFamily(ctx, "fid", exp); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	revoked, err = s.IsFamilyRevoked(ctx, "fid")
	if err != nil {
		t.Fatalf("post-check: %v", err)
	}
	if !revoked {
		t.Fatal("revoked family reports not revoked")
	}
}

func TestMemoryRefreshStoreRevokeFamilyExpires(t *testing.T) {
	s := NewMemoryRefreshStore(0)
	defer s.Close()
	ctx := t.Context()
	now := time.Now()
	s.SetClock(func() time.Time { return now })

	if err := s.RevokeFamily(ctx, "fid", now.Add(time.Minute)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	now = now.Add(2 * time.Minute)
	revoked, err := s.IsFamilyRevoked(ctx, "fid")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if revoked {
		t.Fatal("family still revoked after exp")
	}
}

func TestMemoryRefreshStoreClose(t *testing.T) {
	// Pruner running — exercise the stop path.
	s := NewMemoryRefreshStore(10 * time.Millisecond)
	s.Close()
	s.Close() // idempotent
}

func TestNewTokenIDUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for i := range 64 {
		id, err := newTokenID()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("collision after %d ids: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// Sanity: the dummy `context` import is exercised by the methods.
var _ = context.Background
