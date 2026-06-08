package service_test

import (
	"context"
	stderrors "errors"
	"log/slog"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/internal/testutil"
	"github.com/frankbardon/parsec/service"
	"github.com/frankbardon/parsec/tokenbroker"
)

// TestService_RevokeToken_NoStoreReturnsInvalidArgument confirms operators
// get a coded error (not a silent no-op) when the parsec instance has no
// RevocationStore wired.
func TestService_RevokeToken_NoStoreReturnsInvalidArgument(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()

	err := svc.RevokeToken(context.Background(), "abc", "user-1", "compromised")
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("want *perr.Error, got %T (%v)", err, err)
	}
	if pe.Code != perr.InvalidArgument {
		t.Errorf("code = %s, want %s", pe.Code, perr.InvalidArgument)
	}
}

// TestService_RevokeUser_NoStoreReturnsInvalidArgument mirrors the
// token-level case for the user-blanket revoke path.
func TestService_RevokeUser_NoStoreReturnsInvalidArgument(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()

	err := svc.RevokeUser(context.Background(), "user-1", "")
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("want *perr.Error, got %T (%v)", err, err)
	}
	if pe.Code != perr.InvalidArgument {
		t.Errorf("code = %s, want %s", pe.Code, perr.InvalidArgument)
	}
}

// TestService_RevokeToken_MissingArgument confirms the empty-token path
// is rejected before reaching the store.
func TestService_RevokeToken_MissingArgument(t *testing.T) {
	svc, stop := newServiceWithRevocations(t)
	defer stop()

	err := svc.RevokeToken(context.Background(), "", "", "")
	var pe *perr.Error
	if !stderrors.As(err, &pe) || pe.Code != perr.InvalidArgument {
		t.Errorf("want PARSEC_INVALID_ARGUMENT, got %v", err)
	}
}

// TestService_RevokeUser_MissingArgument is the user-side mirror.
func TestService_RevokeUser_MissingArgument(t *testing.T) {
	svc, stop := newServiceWithRevocations(t)
	defer stop()

	err := svc.RevokeUser(context.Background(), "", "")
	var pe *perr.Error
	if !stderrors.As(err, &pe) || pe.Code != perr.InvalidArgument {
		t.Errorf("want PARSEC_INVALID_ARGUMENT, got %v", err)
	}
}

// TestService_RevokeToken_WritesToStore is the happy-path round-trip:
// RevokeToken populates the store, IsRevoked observes the entry.
func TestService_RevokeToken_WritesToStore(t *testing.T) {
	svc, stop := newServiceWithRevocations(t)
	defer stop()

	const tokenID = "jti-abc"
	if err := svc.RevokeToken(context.Background(), tokenID, "user-1", "compromised"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	store := svc.Parsec().RevocationStore()
	revoked, err := store.IsRevoked(context.Background(), tokenID)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Errorf("IsRevoked = false after RevokeToken; want true")
	}
}

// TestService_RevokeUser_WritesToStore mirrors the round-trip for the
// user-blanket path. The store records a cutoff timestamp; tokens
// issued before it are considered revoked.
func TestService_RevokeUser_WritesToStore(t *testing.T) {
	svc, stop := newServiceWithRevocations(t)
	defer stop()

	const userID = "user-1"
	preIssue := time.Now().UTC().Add(-time.Minute)
	if err := svc.RevokeUser(context.Background(), userID, "session reset"); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}
	store := svc.Parsec().RevocationStore()
	revoked, err := store.IsUserRevoked(context.Background(), userID, preIssue)
	if err != nil {
		t.Fatalf("IsUserRevoked: %v", err)
	}
	if !revoked {
		t.Errorf("IsUserRevoked = false for token issued before cutoff; want true")
	}
}

// newServiceWithRevocations boots a parsec instance with a memory
// revocation store wired so the round-trip tests can observe writes.
func newServiceWithRevocations(t *testing.T) (*service.Service, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	store := tokenbroker.NewMemoryRevocations()
	p, err := parsec.New(parsec.Options{
		KeyRing:         testutil.RingFromSecret(t, secret),
		SweepInterval:   50 * time.Millisecond,
		Logger:          slog.New(slog.DiscardHandler),
		RevocationStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(doneCh)
	}()
	time.Sleep(75 * time.Millisecond)
	cleanup := func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
			t.Logf("warn: parsec.Run did not exit within 2s")
		}
	}
	return service.New(p, testutil.Version), cleanup
}
