package auth

import (
	"context"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/channels"
)

// TestSubscribeAuthorizer_ScopeAllows mints a token whose only
// authorization is a pattern scope and confirms it subscribes to a
// matching channel.
func TestSubscribeAuthorizer_ScopeAllows(t *testing.T) {
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
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := channels.ParseName("private:webapp.user.42.downloads")
	if err := auth(context.Background(), "u", target, centrifuge.SubscribeEvent{Token: tok}); err != nil {
		t.Fatalf("scope-only access should allow matching channel: %v", err)
	}
}

// TestSubscribeAuthorizer_ScopeRejectsNonMatch confirms a non-matching
// channel is denied even when the token carries a scope.
func TestSubscribeAuthorizer_ScopeRejectsNonMatch(t *testing.T) {
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
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := channels.ParseName("private:webapp.user.43.downloads")
	err = auth(context.Background(), "u", target, centrifuge.SubscribeEvent{Token: tok})
	if err == nil {
		t.Fatal("expected non-matching scope to be denied")
	}
}

// TestSubscribeAuthorizer_ScopeRejectsWrongVerb — a scope that only
// grants publish must not let the holder subscribe.
func TestSubscribeAuthorizer_ScopeRejectsWrongVerb(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	now := time.Now()
	tok, err := signer.Sign(Claims{
		Typ: TypeAccess,
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
		Scopes: []Scope{
			{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbPublish}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := channels.ParseName("private:webapp.user.42.downloads")
	err = auth(context.Background(), "u", target, centrifuge.SubscribeEvent{Token: tok})
	if err == nil {
		t.Fatal("publish-only scope must not authorize subscribe")
	}
}

// TestSubscribeAuthorizer_ScopeVisibilityCannotCross — a scope on a
// private namespace must not be honored when the subscribe target is
// public (and vice versa). Because public subscribes bypass the token
// gate entirely, we exercise the inverse: a token with a public scope
// must not authorize a private subscribe.
func TestSubscribeAuthorizer_ScopeVisibilityCannotCross(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	now := time.Now()
	tok, err := signer.Sign(Claims{
		Typ: TypeAccess,
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
		Scopes: []Scope{
			{Pattern: "public:webapp.system.**", Verbs: []Verb{VerbSubscribe}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := channels.ParseName("private:webapp.user.42.downloads")
	err = auth(context.Background(), "u", target, centrifuge.SubscribeEvent{Token: tok})
	if err == nil {
		t.Fatal("public scope must not authorize a private subscribe")
	}
}

// TestSubscribeAuthorizer_ChsOnlyStillWorks confirms a token carrying
// only Chs (no Scopes) is authorized for subscribe on a listed channel.
func TestSubscribeAuthorizer_ChsOnlyStillWorks(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	target, _ := channels.ParseName("private:webapp.session.abc.notifications")
	now := time.Now()
	tok, err := signer.Sign(Claims{
		Typ: TypeAccess, Chs: []string{target.String()},
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth(context.Background(), "u", target, centrifuge.SubscribeEvent{Token: tok}); err != nil {
		t.Fatalf("Chs-only token must still authorize: %v", err)
	}
}
