package tokenbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
)

func mustIssuer(t *testing.T) *auth.Issuer {
	t.Helper()
	ring, err := auth.NewKeyRingFromSecret([]byte("test-secret-1234-abcdefgh-test-1"))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(ring)
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewIssuer(signer)
}

func fixedAuth(uid UserID) Authenticator {
	return AuthenticatorFunc(func(_ context.Context, bearer string) (UserID, error) {
		if bearer == "" {
			return "", ErrUnauthenticated
		}
		return uid, nil
	})
}

func TestBrokerIssueSingleChannel(t *testing.T) {
	b, err := New(Options{
		Issuer:        mustIssuer(t),
		Authorizer:    AllowAll,
		Authenticator: fixedAuth("user-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.Issue(context.Background(), "u-token", IssueRequest{
		Channels: []string{"public:app.dom.id"},
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" || resp.TokenID == "" {
		t.Fatalf("empty token: %+v", resp)
	}
	if len(resp.ChannelsGranted) != 1 {
		t.Fatalf("granted: %v", resp.ChannelsGranted)
	}
}

func TestBrokerIssueMultiChannel(t *testing.T) {
	b, err := New(Options{
		Issuer:        mustIssuer(t),
		Authorizer:    AllowAll,
		Authenticator: fixedAuth("user-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	chs := []string{"public:a.b.c", "public:a.b.d"}
	resp, err := b.Issue(context.Background(), "u-token", IssueRequest{Channels: chs, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ChannelsGranted) != 2 {
		t.Fatalf("expected 2 granted: %v", resp.ChannelsGranted)
	}
}

func TestBrokerDenial(t *testing.T) {
	ar := AuthorizerFunc(func(_ context.Context, _ UserID, ch []string) AuthDecision {
		var d AuthDecision
		for _, c := range ch {
			d.Denied = append(d.Denied, DeniedChannel{Channel: c, Reason: "test denial"})
		}
		return d
	})
	b, err := New(Options{Issuer: mustIssuer(t), Authorizer: ar, Authenticator: fixedAuth("u")})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.Issue(context.Background(), "u-tok", IssueRequest{
		Channels: []string{"x"}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Token != "" {
		t.Fatal("expected no token when fully denied")
	}
	if len(resp.ChannelsDenied) != 1 {
		t.Fatalf("denied: %v", resp.ChannelsDenied)
	}
}

func TestBrokerDelegate(t *testing.T) {
	b, err := New(Options{
		Issuer:        mustIssuer(t),
		Authorizer:    AllowAll,
		Authenticator: fixedAuth("svc-1"),
		DelegateAuthorizer: func(_ context.Context, svc, target UserID) error {
			if svc == "svc-1" && target == "u-1" {
				return nil
			}
			return ErrUnauthenticated
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.Delegate(context.Background(), "svc-tok", DelegateRequest{
		OnBehalfOf:      "u-1",
		Channels:        []string{"public:a.b.c"},
		LifetimeSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" {
		t.Fatal("expected token")
	}
}

func TestBrokerRevocation(t *testing.T) {
	rev := NewMemoryRevocations()
	b, err := New(Options{
		Issuer: mustIssuer(t), Authorizer: AllowAll,
		Authenticator: fixedAuth("u"), Revocations: rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Revoke(context.Background(), "u-tok", RevokeRequest{TokenID: "abc"}); err != nil {
		t.Fatal(err)
	}
	yes, _ := b.IsRevoked(context.Background(), "abc", "", time.Now())
	if !yes {
		t.Fatal("expected revoked")
	}
	if err := b.Revoke(context.Background(), "u-tok", RevokeRequest{UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	yes, _ = b.IsRevoked(context.Background(), "", "u", time.Now().Add(-time.Hour))
	if !yes {
		t.Fatal("user revoke should affect pre-revoke tokens")
	}
	yes, _ = b.IsRevoked(context.Background(), "", "u", time.Now().Add(time.Hour))
	if yes {
		t.Fatal("user revoke should NOT affect tokens issued in the future")
	}
}

func TestBrokerHTTP(t *testing.T) {
	b, err := New(Options{
		Issuer: mustIssuer(t), Authorizer: AllowAll,
		Authenticator: fixedAuth("u-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	body := bytes.NewReader([]byte(`{"channels":["public:a.b.c"],"ttl":3600000000000}`))
	req, _ := http.NewRequest("POST", srv.URL+"/token", body)
	req.Header.Set("Authorization", "Bearer u-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var out IssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" {
		t.Fatal("empty token")
	}

	// Missing bearer.
	resp, err = http.Post(srv.URL+"/token", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestMemoryRevocationsExpiresAndPrunes(t *testing.T) {
	now := time.Now()
	clock := now
	rev := NewMemoryRevocations().WithMaxTTL(time.Minute)
	rev.SetClock(func() time.Time { return clock })

	if err := rev.Revoke(context.Background(), "tok-fresh", "u-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := rev.RevokeAllForUser(context.Background(), "u-2"); err != nil {
		t.Fatal(err)
	}

	// Right after revoke — still revoked.
	yes, _ := rev.IsRevoked(context.Background(), "tok-fresh")
	if !yes {
		t.Fatal("expected fresh revoke to be honored")
	}
	yes, _ = rev.IsUserRevoked(context.Background(), "u-2", now.Add(-time.Second))
	if !yes {
		t.Fatal("expected fresh user revoke to be honored")
	}

	// Advance the clock past MaxTTL — entries should age out on read.
	clock = now.Add(2 * time.Minute)
	yes, _ = rev.IsRevoked(context.Background(), "tok-fresh")
	if yes {
		t.Fatal("expected aged token to be unrevoked")
	}
	yes, _ = rev.IsUserRevoked(context.Background(), "u-2", now.Add(-time.Second))
	if yes {
		t.Fatal("expected aged user revoke to be unrevoked")
	}

	// Prune should shrink the maps.
	rev.Prune(clock)
	rev.mu.RLock()
	defer rev.mu.RUnlock()
	if len(rev.byToken) != 0 || len(rev.byUserAt) != 0 {
		t.Fatalf("prune left entries: token=%d user=%d", len(rev.byToken), len(rev.byUserAt))
	}
}

func TestMemoryRevocationsRejectsEmptyArgs(t *testing.T) {
	rev := NewMemoryRevocations()
	if err := rev.Revoke(context.Background(), "", "u", ""); err == nil {
		t.Fatal("expected empty tokenID rejected")
	}
	if err := rev.RevokeAllForUser(context.Background(), ""); err == nil {
		t.Fatal("expected empty userID rejected")
	}
}

func TestRoleAuthorizer(t *testing.T) {
	ra := &RoleAuthorizer{
		UserRoles: func(_ context.Context, _ UserID) []string { return []string{"reader"} },
		RolePatterns: map[string][]string{
			"reader": {"public:reports.**"},
		},
	}
	d := ra.Authorize(context.Background(), "u", []string{"public:reports.x.y", "public:secrets.x"})
	if len(d.Granted) != 1 || d.Granted[0] != "public:reports.x.y" {
		t.Fatalf("granted: %v", d.Granted)
	}
	if len(d.Denied) != 1 || d.Denied[0].Channel != "public:secrets.x" {
		t.Fatalf("denied: %v", d.Denied)
	}
}
