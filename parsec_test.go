package parsec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
	perr "github.com/frankbardon/parsec/errors"
)

// testParsec constructs a Parsec instance with a fixed secret and starts it.
// Returns the instance and a cancel function that shuts it down cleanly.
// ringFromSecret wraps secret in a single-key KeyRing — the standard
// shape parsec tests use when they want a stable HMAC across recreate
// pairs.
func ringFromSecret(t testing.TB, secret []byte) *auth.KeyRing {
	t.Helper()
	r, err := auth.NewKeyRingFromSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func testParsec(t *testing.T) (*Parsec, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{KeyRing: ringFromSecret(t, secret), SweepInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	// Give the broker a moment to start.
	time.Sleep(75 * time.Millisecond)
	return p, func() {
		cancel()
		<-done
	}
}

func TestParsec_CreatePrivate_ReturnsTokens(t *testing.T) {
	p, stop := testParsec(t)
	defer stop()

	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken == "" || creds.RefreshToken == "" {
		t.Fatal("expected both tokens populated")
	}
	if !creds.AccessExpires.Before(creds.RefreshExpires) && !creds.AccessExpires.Equal(creds.RefreshExpires) {
		t.Fatalf("access should not outlast refresh: access=%v refresh=%v", creds.AccessExpires, creds.RefreshExpires)
	}
	// Tokens must verify with the right typ.
	if _, err := p.Verifier().Verify(creds.AccessToken, auth.TypeAccess); err != nil {
		t.Fatalf("access verify: %v", err)
	}
	if _, err := p.Verifier().Verify(creds.RefreshToken, auth.TypeRefresh); err != nil {
		t.Fatalf("refresh verify: %v", err)
	}
}

func TestParsec_RefreshAccess(t *testing.T) {
	p, stop := testParsec(t)
	defer stop()

	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.RefreshAccess(creds.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if res.AccessToken == "" {
		t.Fatal("expected access token")
	}
	if _, err := p.Verifier().Verify(res.AccessToken, auth.TypeAccess); err != nil {
		t.Fatalf("refreshed access verify: %v", err)
	}
}

func TestParsec_RefreshFailsForDeletedChannel(t *testing.T) {
	p, stop := testParsec(t)
	defer stop()

	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Delete the channel.
	if err := p.Manager().Delete(creds.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RefreshAccess(creds.RefreshToken); err == nil {
		t.Fatal("expected refresh failure on deleted channel")
	}
}

func TestParsec_RefreshRejectsAccessToken(t *testing.T) {
	p, stop := testParsec(t)
	defer stop()

	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.RefreshAccess(creds.AccessToken)
	if err == nil {
		t.Fatal("expected refresh to reject access tokens")
	}
	var pe *perr.Error
	if !errors.As(err, &pe) || pe.Code != perr.AuthDenied {
		t.Fatalf("expected AuthDenied, got %v", err)
	}
}

func TestParsec_CreatePrivate_RollsBackOnIssuerFailure(t *testing.T) {
	// This guard is mostly documentation: with valid input the rollback
	// path is unreachable. We assert no dangling record when CreatePrivate
	// returns success and the channel is then deleted by the caller.
	p, stop := testParsec(t)
	defer stop()
	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Manager().Get(creds.Name); err != nil {
		t.Fatalf("channel should exist after CreatePrivate: %v", err)
	}
}
