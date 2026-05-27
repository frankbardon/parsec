package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/channels"
)

func TestSubscribeAuthorizer_PublicAllowedAlways(t *testing.T) {
	ring := testRing(t)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	pub, _ := channels.ParseName("public:webapp.system.status")
	if err := auth(context.Background(), "u", pub, centrifuge.SubscribeEvent{}); err != nil {
		t.Fatalf("public should always allow: %v", err)
	}
}

func TestSubscribeAuthorizer_PrivateRequiresToken(t *testing.T) {
	ring := testRing(t)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	priv, _ := channels.ParseName("private:webapp.session.abc.notifications")
	err := auth(context.Background(), "u", priv, centrifuge.SubscribeEvent{})
	if err == nil {
		t.Fatal("expected token-required rejection")
	}
}

func TestSubscribeAuthorizer_PrivateAcceptsScoped(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	priv, _ := channels.ParseName("private:webapp.session.abc.notifications")
	now := time.Now()
	tok, _ := signer.Sign(Claims{
		Typ: TypeAccess, Chs: []string{priv.String()},
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
	})
	if err := auth(context.Background(), "u", priv, centrifuge.SubscribeEvent{Token: tok}); err != nil {
		t.Fatalf("scoped access should allow: %v", err)
	}
}

func TestSubscribeAuthorizer_RejectsChannelMismatch(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	priv, _ := channels.ParseName("private:webapp.session.abc.notifications")
	now := time.Now()
	tok, _ := signer.Sign(Claims{
		Typ: TypeAccess, Chs: []string{"private:webapp.session.xyz.notifications"},
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
	})
	err := auth(context.Background(), "u", priv, centrifuge.SubscribeEvent{Token: tok})
	if err == nil || !strings.Contains(err.Error(), "not authorize") {
		t.Fatalf("expected channel-mismatch rejection, got %v", err)
	}
}

func TestSubscribeAuthorizer_RejectsRefreshTokenAtSubscribe(t *testing.T) {
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	v, _ := NewVerifier(ring)
	auth := NewSubscribeAuthorizer(v)
	priv, _ := channels.ParseName("private:webapp.session.abc.notifications")
	now := time.Now()
	tok, _ := signer.Sign(Claims{
		Typ: TypeRefresh, Chs: []string{priv.String()},
		Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
	})
	err := auth(context.Background(), "u", priv, centrifuge.SubscribeEvent{Token: tok})
	if err == nil {
		t.Fatal("expected refresh-at-subscribe rejection")
	}
}
