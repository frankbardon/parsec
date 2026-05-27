package auth

import "testing"

// TestClaims_Authorizes_ChsOnly verifies that a Claims carrying only
// the Chs list and no Scopes authorizes every verb on the listed names.
func TestClaims_Authorizes_ChsOnly(t *testing.T) {
	c := Claims{Chs: []string{"private:webapp.session.abc.notifications"}}
	if !c.Authorizes("private:webapp.session.abc.notifications", VerbSubscribe) {
		t.Error("exact Chs entry should authorize any verb")
	}
	if !c.Authorizes("private:webapp.session.abc.notifications", VerbPublish) {
		t.Error("exact Chs entry should authorize publish too")
	}
	if c.Authorizes("private:webapp.session.xyz.notifications", VerbSubscribe) {
		t.Error("Chs is exact-match only — should not authorize a different name")
	}
}

func TestClaims_Authorizes_ScopeOnly(t *testing.T) {
	c := Claims{
		Scopes: []Scope{
			{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbSubscribe, VerbPublish}},
		},
	}
	if !c.Authorizes("private:webapp.user.42.downloads", VerbSubscribe) {
		t.Error("scope should authorize a matching channel for the listed verb")
	}
	if !c.Authorizes("private:webapp.user.42.profile", VerbPublish) {
		t.Error("scope should authorize publish on a matching channel")
	}
	if c.Authorizes("private:webapp.user.42.downloads", VerbManage) {
		t.Error("verb not in scope list — must deny")
	}
	if c.Authorizes("private:webapp.user.43.downloads", VerbSubscribe) {
		t.Error("non-matching pattern must deny")
	}
}

func TestClaims_Authorizes_UnionOfChsAndScopes(t *testing.T) {
	c := Claims{
		Chs: []string{"private:legacy.user.7.notifications"},
		Scopes: []Scope{
			{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbSubscribe}},
		},
	}
	// Chs path still works.
	if !c.Authorizes("private:legacy.user.7.notifications", VerbSubscribe) {
		t.Error("union token should still honor Chs entries")
	}
	// Scopes path adds new authorization.
	if !c.Authorizes("private:webapp.user.42.downloads", VerbSubscribe) {
		t.Error("union token should also honor scope patterns")
	}
	// Neither matches.
	if c.Authorizes("private:webapp.user.43.downloads", VerbSubscribe) {
		t.Error("channel that satisfies neither Chs nor Scopes must be denied")
	}
}

func TestClaims_Authorizes_MalformedChannelDenied(t *testing.T) {
	c := Claims{
		Scopes: []Scope{{Pattern: "private:foo.*", Verbs: []Verb{VerbSubscribe}}},
	}
	if c.Authorizes("not-a-channel", VerbSubscribe) {
		t.Error("malformed channel input must fail closed when only scopes apply")
	}
}

func TestClaims_Authorizes_EmptyClaimsAlwaysDenied(t *testing.T) {
	var c Claims
	if c.Authorizes("private:foo.bar.7", VerbSubscribe) {
		t.Error("empty claims must deny everything")
	}
}

