package parsec

import (
	"context"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/channels"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/tokenbroker"
)

// testParsecWithRevocations boots a parsec instance whose subscribe
// authorizer is wired to revStore. Returns the running instance and a
// stop function.
func testParsecWithRevocations(t *testing.T, revStore tokenbroker.RevocationStore) (*Parsec, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{
		KeyRing:         ringFromSecret(t, secret),
		SweepInterval:   50 * time.Millisecond,
		RevocationStore: revStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	time.Sleep(75 * time.Millisecond)
	return p, func() {
		cancel()
		<-done
	}
}

func TestRevocation_SubscribeDeniedAfterTokenRevoke(t *testing.T) {
	revStore := tokenbroker.NewMemoryRevocations()
	p, stop := testParsecWithRevocations(t, revStore)
	defer stop()

	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	chName, err := channels.ParseName("private:test.user.1.notifications")
	if err != nil {
		t.Fatal(err)
	}
	authz := p.Broker().SubscribeAuthorizer()
	ctx := context.Background()
	event := centrifuge.SubscribeEvent{Token: creds.AccessToken, Channel: chName.String()}

	// First subscribe must succeed.
	if err := authz(ctx, "user-1", chName, event); err != nil {
		t.Fatalf("expected fresh token to authorize, got %v", err)
	}

	// Decode the JTI from the access token, revoke it, then retry — must deny.
	claims, err := p.Verifier().Verify(creds.AccessToken, auth.TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if claims.JTI == "" {
		t.Fatal("access token missing JTI — revocation cannot work")
	}
	if err := revStore.Revoke(ctx, claims.JTI, "user-1", "compromised"); err != nil {
		t.Fatal(err)
	}
	err = authz(ctx, "user-1", chName, event)
	if err == nil {
		t.Fatal("expected revoked token to be denied")
	}
	pe, ok := err.(*perr.Error)
	if !ok || pe.Code != perr.AuthDenied {
		t.Fatalf("err = %v, want PARSEC_AUTH_DENIED", err)
	}
}

func TestRevocation_SubscribeDeniedAfterUserBlanketRevoke(t *testing.T) {
	revStore := tokenbroker.NewMemoryRevocations()
	p, stop := testParsecWithRevocations(t, revStore)
	defer stop()

	creds, err := p.CreatePrivate("user-7", "private:test.user.7.x", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	chName, _ := channels.ParseName("private:test.user.7.x")
	authz := p.Broker().SubscribeAuthorizer()
	ctx := context.Background()
	event := centrifuge.SubscribeEvent{Token: creds.AccessToken, Channel: chName.String()}

	// Fresh token authorized.
	if err := authz(ctx, "user-7", chName, event); err != nil {
		t.Fatalf("expected fresh token to authorize, got %v", err)
	}

	// Blanket-revoke the user a full second later — clock granularity
	// is per-nanosecond so this guarantees the token's iat falls before
	// the revoke moment even on systems with low-res clocks.
	time.Sleep(time.Second)
	if err := revStore.RevokeAllForUser(ctx, "user-7"); err != nil {
		t.Fatal(err)
	}
	err = authz(ctx, "user-7", chName, event)
	if err == nil {
		t.Fatal("expected blanket-revoked user to be denied")
	}
	pe, ok := err.(*perr.Error)
	if !ok || pe.Code != perr.AuthDenied {
		t.Fatalf("err = %v, want PARSEC_AUTH_DENIED", err)
	}
}

func TestRevocation_NoStoreLeavesSubscribeUnaffected(t *testing.T) {
	// Sanity: without RevocationStore, the subscribe authorizer never
	// consults revocation state — the existing token / scope flow is
	// the only gate.
	p, stop := testParsec(t)
	defer stop()
	creds, err := p.CreatePrivate("user-1", "private:test.user.1.notifications", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	chName, _ := channels.ParseName("private:test.user.1.notifications")
	authz := p.Broker().SubscribeAuthorizer()
	event := centrifuge.SubscribeEvent{Token: creds.AccessToken, Channel: chName.String()}
	if err := authz(context.Background(), "user-1", chName, event); err != nil {
		t.Fatalf("authorize without RevocationStore: %v", err)
	}
}
