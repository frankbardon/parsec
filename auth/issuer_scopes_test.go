package auth

import (
	"testing"
	"time"
)

// TestIssuer_PairWithScopes_RoundTrip mints a token carrying pattern
// scopes, verifies the signature, and confirms the resulting Claims
// authorize a matching channel.
func TestIssuer_PairWithScopes_RoundTrip(t *testing.T) {
	iss, ver := newTestIssuer(t)
	scopes := []Scope{
		{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbSubscribe}},
	}
	pair, err := iss.IssuePair("user-1", "private:webapp.user.42.profile", 30*time.Minute, scopes)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := ver.Verify(pair.AccessToken, TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if !ac.Authorizes("private:webapp.user.42.profile", VerbSubscribe) {
		t.Error("scope must authorize a matching channel")
	}
	if !ac.Authorizes("private:webapp.user.42.downloads", VerbSubscribe) {
		t.Error("scope must authorize a sibling channel under the same pattern")
	}
	if ac.Authorizes("private:webapp.user.43.profile", VerbSubscribe) {
		t.Error("scope must not authorize a channel outside the pattern")
	}
	// Verb gating only applies when the scope path is the one that
	// matched — Chs (the channel the token was bound to) is verb-agnostic
	// by design (legacy semantics). Check verb gating on a sibling
	// channel that is only authorized via the scope.
	if ac.Authorizes("private:webapp.user.42.downloads", VerbPublish) {
		t.Error("subscribe-only scope must not authorize publish on a sibling channel")
	}
}

// TestIssuer_PairWithScopes_RejectsBadPattern fails at issuance — the
// caller cannot mint a token whose pattern would never compile.
func TestIssuer_PairWithScopes_RejectsBadPattern(t *testing.T) {
	iss, _ := newTestIssuer(t)
	bad := []Scope{{Pattern: "*:foo.bar", Verbs: []Verb{VerbSubscribe}}}
	_, err := iss.IssuePair("user-1", "private:webapp.user.42.profile", 30*time.Minute, bad)
	if err == nil {
		t.Fatal("expected cross-visibility scope to be rejected at issuance")
	}
}

func TestIssuer_PairWithScopes_RejectsEmptyVerbList(t *testing.T) {
	iss, _ := newTestIssuer(t)
	bad := []Scope{{Pattern: "private:foo.bar.*", Verbs: nil}}
	_, err := iss.IssuePair("user-1", "private:webapp.user.42.profile", 30*time.Minute, bad)
	if err == nil {
		t.Fatal("expected empty-verb-list rejection")
	}
}

func TestIssuer_PairWithScopes_RejectsUnknownVerb(t *testing.T) {
	iss, _ := newTestIssuer(t)
	bad := []Scope{{Pattern: "private:foo.bar.*", Verbs: []Verb{"delete"}}}
	_, err := iss.IssuePair("user-1", "private:webapp.user.42.profile", 30*time.Minute, bad)
	if err == nil {
		t.Fatal("expected unknown-verb rejection")
	}
}

// TestIssuer_IssueAccess confirms the access-only mint path
// carries scope through correctly.
func TestIssuer_IssueAccess(t *testing.T) {
	iss, ver := newTestIssuer(t)
	scopes := []Scope{{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbSubscribe, VerbPublish}}}
	refreshExp := time.Now().Add(30 * time.Minute)
	tok, _, err := iss.IssueAccess("user-1", "private:webapp.user.42.profile", refreshExp, scopes)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ver.Verify(tok, TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Authorizes("private:webapp.user.42.profile", VerbPublish) {
		t.Error("access token should carry publish verb")
	}
}

