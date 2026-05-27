package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/channels"
	perr "github.com/frankbardon/parsec/errors"
)

// TestSubscribeAuthorizer_DenyScopeRejects mints a token with an allow
// over the user prefix and a deny on a sub-namespace. Subscribing to a
// channel in the denied sub-namespace returns PARSEC_AUTH_DENIED even
// though the allow scope would otherwise permit it. Channel names stay
// within the 4-segment private grammar (<app>.<domain>.<id>.<topic>).
func TestSubscribeAuthorizer_DenyScopeRejects(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	now := time.Now()
	tok, err := signer.Sign(Claims{
		Typ: TypeAccess,
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
		Scopes: []Scope{
			{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbSubscribe}},
			{Pattern: "private:webapp.user.42.financials", Verbs: []Verb{VerbSubscribe}, Deny: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Allowed channel — outside the deny pattern.
	okTarget, err := channels.ParseName("private:webapp.user.42.profile")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth(context.Background(), "u", okTarget, centrifuge.SubscribeEvent{Token: tok}); err != nil {
		t.Fatalf("non-denied channel must subscribe: %v", err)
	}
	// Denied channel — matches the deny pattern exactly.
	bad, err := channels.ParseName("private:webapp.user.42.financials")
	if err != nil {
		t.Fatal(err)
	}
	denyErr := auth(context.Background(), "u", bad, centrifuge.SubscribeEvent{Token: tok})
	if denyErr == nil {
		t.Fatal("denied channel must be rejected by subscribe authorizer")
	}
	// Returned error must be a *perr.Error coded AUTH_DENIED.
	var pe *perr.Error
	if !errors.As(denyErr, &pe) {
		t.Fatalf("expected *perr.Error at boundary; got %T (%v)", denyErr, denyErr)
	}
	if pe.Code != perr.AuthDenied {
		t.Errorf("expected PARSEC_AUTH_DENIED code; got %s", pe.Code)
	}
}

// TestSubscribeAuthorizer_DenyBeatsChs — when a token's Chs entry
// names the requested channel AND a deny scope also matches it, the
// deny still wins. Mirrors TestClaims_Authorizes_DenyOverridesChs at
// the subscribe-authorizer layer.
func TestSubscribeAuthorizer_DenyBeatsChs(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	now := time.Now()
	target, err := channels.ParseName("private:webapp.user.42.financials")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := signer.Sign(Claims{
		Typ: TypeAccess,
		Chs: []string{target.String()},
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
		Scopes: []Scope{
			{Pattern: "private:webapp.user.42.financials", Verbs: []Verb{VerbSubscribe}, Deny: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth(context.Background(), "u", target, centrifuge.SubscribeEvent{Token: tok}); err == nil {
		t.Fatal("deny scope must override Chs entry for subscribe")
	}
}

// TestSubscribeAuthorizer_DenyDoesNotBlockUnrelatedChannel — a deny on
// pattern A must not block a subscribe to pattern B. Guards against
// an overly-greedy implementation that short-circuited on the first
// deny scope seen.
func TestSubscribeAuthorizer_DenyDoesNotBlockUnrelatedChannel(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	now := time.Now()
	tok, err := signer.Sign(Claims{
		Typ: TypeAccess,
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
		Scopes: []Scope{
			{Pattern: "private:tenant.99.**", Verbs: []Verb{VerbSubscribe}, Deny: true},
			{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbSubscribe}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := channels.ParseName("private:webapp.user.42.profile")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth(context.Background(), "u", target, centrifuge.SubscribeEvent{Token: tok}); err != nil {
		t.Fatalf("unrelated deny must not block this subscribe: %v", err)
	}
}
