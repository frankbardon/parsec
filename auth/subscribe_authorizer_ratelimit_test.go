package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/channels"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/ratelimit"
)

func TestSubscribeAuthorizer_RateLimited(t *testing.T) {
	// Burst 2, then 3rd subscribe attempt should hit PARSEC_RATE_LIMITED.
	ring := auth.NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(ring)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(ring)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := channels.ParseName("private:webapp.session.abc.notifications")
	now := time.Now()
	tok, err := signer.Sign(auth.Claims{
		Typ: auth.TypeAccess, Chs: []string{priv.String()},
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}

	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 2, Per: time.Second})
	authorize := auth.NewSubscribeAuthorizerWithLimiter(v, lim, ratelimit.Limit{Rate: 2, Per: time.Second})

	for i := 0; i < 2; i++ {
		if err := authorize(context.Background(), "alice", priv, centrifuge.SubscribeEvent{Token: tok}); err != nil {
			t.Fatalf("iter=%d unexpected err: %v", i, err)
		}
	}
	err = authorize(context.Background(), "alice", priv, centrifuge.SubscribeEvent{Token: tok})
	if err == nil {
		t.Fatal("expected rate-limit error on 3rd call")
	}
	pe, ok := err.(*perr.Error)
	if !ok {
		t.Fatalf("err type = %T, want *perr.Error", err)
	}
	if pe.Code != perr.RateLimited {
		t.Fatalf("Code = %s, want %s", pe.Code, perr.RateLimited)
	}
}

func TestSubscribeAuthorizer_AnonymousAllowedWhenNoUserID(t *testing.T) {
	// userID="" means the connection isn't authenticated yet (subscribe-time
	// token-only flow); the authorizer should not charge the bucket and
	// should fall through to its normal logic — for a public channel this
	// means allow.
	ring := auth.NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(ring)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := channels.ParseName("public:webapp.system.status")
	lim := ratelimit.NewMemoryLimiter(ratelimit.Limit{Rate: 1, Per: time.Second})
	authorize := auth.NewSubscribeAuthorizerWithLimiter(v, lim, ratelimit.Limit{Rate: 1, Per: time.Second})
	for i := 0; i < 10; i++ {
		if err := authorize(context.Background(), "", pub, centrifuge.SubscribeEvent{}); err != nil {
			t.Fatalf("anonymous-public iter=%d should allow, got %v", i, err)
		}
	}
}

func TestSubscribeAuthorizer_NoLimiterUnchangedBehavior(t *testing.T) {
	// Passing nil limiter should be byte-identical to NewSubscribeAuthorizer.
	ring := auth.NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		t.Fatal(err)
	}
	v, _ := auth.NewVerifier(ring)
	authNoLim := auth.NewSubscribeAuthorizerWithLimiter(v, nil, ratelimit.Limit{})
	authOrig := auth.NewSubscribeAuthorizer(v)
	pub, _ := channels.ParseName("public:webapp.system.status")
	ctx := context.Background()
	err1 := authNoLim(ctx, "alice", pub, centrifuge.SubscribeEvent{})
	err2 := authOrig(ctx, "alice", pub, centrifuge.SubscribeEvent{})
	if (err1 == nil) != (err2 == nil) {
		t.Fatalf("limiter=nil should match original: %v vs %v", err1, err2)
	}
}
