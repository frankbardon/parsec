package auth

import (
	"errors"
	"testing"
	"time"
)

func newTestIssuer(t *testing.T) (*Issuer, *Verifier) {
	t.Helper()
	ring := testRing(t)
	signer, _ := NewSigner(ring)
	verifier, _ := NewVerifier(ring)
	return NewIssuer(signer), verifier
}

func TestIssuer_Pair_TTLBoundedByChannel(t *testing.T) {
	iss, _ := newTestIssuer(t)
	pair, err := iss.IssuePair("user-1", "private:test.x.1", 10*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pair.RefreshExpires.Sub(pair.AccessExpires) <= 0 {
		t.Fatalf("refresh should outlast access: access=%v refresh=%v", pair.AccessExpires, pair.RefreshExpires)
	}
	if delta := time.Until(pair.RefreshExpires); delta > 11*time.Minute {
		t.Fatalf("refresh outran channel TTL: %v", delta)
	}
}

func TestIssuer_Pair_TTLBoundedByMaxRefresh(t *testing.T) {
	iss, _ := newTestIssuer(t)
	pair, err := iss.IssuePair("user-1", "private:test.x.1", 10*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if delta := time.Until(pair.RefreshExpires); delta > iss.MaxRefreshTTL+time.Minute {
		t.Fatalf("refresh outran MaxRefreshTTL: %v", delta)
	}
}

func TestIssuer_RefreshFlow(t *testing.T) {
	iss, verifier := newTestIssuer(t)
	pair, err := iss.IssuePair("user-1", "private:test.x.1", 30*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := verifier.Verify(pair.RefreshToken, TypeRefresh)
	if err != nil {
		t.Fatal(err)
	}
	if !rc.Authorizes("private:test.x.1", VerbSubscribe) {
		t.Fatal("refresh should carry channel")
	}
	access, exp, err := iss.IssueAccess(rc.Sub, rc.Chs[0], rc.ExpiresAt(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.After(time.Now()) {
		t.Fatal("new access should expire in the future")
	}
	ac, err := verifier.Verify(access, TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if !ac.Authorizes("private:test.x.1", VerbSubscribe) {
		t.Fatal("new access should authorize the channel")
	}
}

func TestIssuer_RefreshExpiredFails(t *testing.T) {
	iss, _ := newTestIssuer(t)
	past := time.Now().Add(-time.Minute)
	_, _, err := iss.IssueAccess("user-1", "private:test.x.1", past, nil)
	if err == nil {
		t.Fatal("expected expired-refresh error")
	}
}

func TestIssuer_Mgmt(t *testing.T) {
	iss, verifier := newTestIssuer(t)
	tok, _, err := iss.IssueMgmt("op-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := verifier.Verify(tok, TypeMgmt)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sub != "op-1" {
		t.Fatal("sub mismatch")
	}
	if _, err := verifier.Verify(tok, TypeAccess); !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("expected type mismatch, got %v", err)
	}
}

func TestIssuer_RejectsBadInputs(t *testing.T) {
	iss, _ := newTestIssuer(t)
	if _, err := iss.IssuePair("", "", 0, nil); err == nil {
		t.Fatal("expected empty channel rejection")
	}
	if _, err := iss.IssuePair("u", "private:test.x.1", 0, nil); err == nil {
		t.Fatal("expected zero channelTTL rejection")
	}
}
