package auth

import "testing"

// Phase 20 — negative ACL scopes. The tests in this file pin down the
// deny-wins precedence introduced by Scope.Deny. They sit beside
// scope_test.go (allow-only behavior) and claims_scopes_test.go
// (union of Chs and allow Scopes) — both must continue to pass.

// TestClaims_Authorizes_DenyNarrowerThanAllow exercises the canonical
// case from the Phase 20 spec: allow `private:foo.bar.**` AND deny
// `private:foo.bar.financials.**`. The deny pattern is strictly
// narrower so `foo.bar.7` is allowed but `foo.bar.financials.x` is
// not. Channel names use the full `<app>.<domain>.<id>` form because
// the private grammar requires three segments minimum (see
// channels/name.go).
func TestClaims_Authorizes_DenyNarrowerThanAllow(t *testing.T) {
	c := Claims{
		Scopes: []Scope{
			{Pattern: "private:webapp.user.financials.**", Verbs: []Verb{VerbSubscribe}, Deny: true},
			{Pattern: "private:webapp.user.**", Verbs: []Verb{VerbSubscribe}},
		},
	}
	if !c.Authorizes("private:webapp.user.42", VerbSubscribe) {
		t.Error("user.42 is inside allow, outside deny — must be authorized")
	}
	if !c.Authorizes("private:webapp.user.42.profile", VerbSubscribe) {
		t.Error("user.42.profile is inside allow, outside deny — must be authorized")
	}
	if c.Authorizes("private:webapp.user.financials.q1", VerbSubscribe) {
		t.Error("financials.q1 is inside deny — must be denied even though allow matches")
	}
	if c.Authorizes("private:webapp.user.financials.q1.detail", VerbSubscribe) {
		t.Error("financials.q1.detail matches deny ** — must be denied")
	}
}

// TestClaims_Authorizes_DenyExactlyMatchesAllow — deny on the same
// pattern as allow with the same verb list: deny wins.
func TestClaims_Authorizes_DenyExactlyMatchesAllow(t *testing.T) {
	c := Claims{
		Scopes: []Scope{
			{Pattern: "private:tenant.42.**", Verbs: []Verb{VerbSubscribe, VerbPublish}},
			{Pattern: "private:tenant.42.**", Verbs: []Verb{VerbSubscribe, VerbPublish}, Deny: true},
		},
	}
	if c.Authorizes("private:tenant.42.alerts", VerbSubscribe) {
		t.Error("overlapping deny on same pattern must override allow")
	}
	if c.Authorizes("private:tenant.42.metrics", VerbPublish) {
		t.Error("overlapping deny on same pattern must override allow (publish verb)")
	}
}

// TestClaims_Authorizes_DenyWithoutOverlappingAllow — a deny without any
// allow scope is still a deny. The principal had no grant to begin with;
// the deny stays defensive and does not unlock anything.
func TestClaims_Authorizes_DenyWithoutOverlappingAllow(t *testing.T) {
	c := Claims{
		Scopes: []Scope{
			{Pattern: "private:tenant.99.**", Verbs: []Verb{VerbSubscribe}, Deny: true},
		},
	}
	// Inside the deny pattern: denied (deny wins, no allow).
	if c.Authorizes("private:tenant.99.metrics", VerbSubscribe) {
		t.Error("deny without overlapping allow still denies the matched channel")
	}
	// Outside the deny pattern: also denied (no allow source at all).
	if c.Authorizes("private:tenant.42.metrics", VerbSubscribe) {
		t.Error("channel outside the deny pattern is denied because no allow grants it either")
	}
}

// TestClaims_Authorizes_DenyOverridesChs — the spec is explicit:
// deny ALWAYS wins. A token whose Chs entry exactly matches a channel
// CAN still be denied if a deny scope's pattern also matches that
// channel and lists the verb being checked. This is intentional — it
// is how operators carve a hole through a permissive Chs list during
// incident response or per-user lockouts. Documented in
// docs/src/channels/acl.md.
func TestClaims_Authorizes_DenyOverridesChs(t *testing.T) {
	c := Claims{
		Chs: []string{"private:tenant.42.financials"},
		Scopes: []Scope{
			{Pattern: "private:tenant.42.financials", Verbs: []Verb{VerbSubscribe}, Deny: true},
		},
	}
	if c.Authorizes("private:tenant.42.financials", VerbSubscribe) {
		t.Error("deny scope must override an exact Chs entry — deny ALWAYS wins per spec")
	}
	// A different verb on the same Chs entry survives because the deny
	// only names subscribe.
	if !c.Authorizes("private:tenant.42.financials", VerbPublish) {
		t.Error("Chs entry should still authorize verbs the deny does not list")
	}
}

// TestClaims_Authorizes_DenyVerbScoped — denying subscribe does not
// deny publish on the same channel. Verb gating applies to both
// passes uniformly.
func TestClaims_Authorizes_DenyVerbScoped(t *testing.T) {
	c := Claims{
		Scopes: []Scope{
			{Pattern: "private:tenant.42.**", Verbs: []Verb{VerbSubscribe, VerbPublish}},
			{Pattern: "private:tenant.42.**", Verbs: []Verb{VerbSubscribe}, Deny: true},
		},
	}
	if c.Authorizes("private:tenant.42.alerts", VerbSubscribe) {
		t.Error("subscribe was denied")
	}
	if !c.Authorizes("private:tenant.42.alerts", VerbPublish) {
		t.Error("publish was not in the deny verb list — must remain authorized")
	}
}

// TestClaims_Authorizes_ChsOnly_NoScopes — a token carrying only Chs
// (no Scopes field) authorizes every verb on its listed channels and
// rejects every other name. The deny pass is a no-op when there are no
// scopes to walk.
func TestClaims_Authorizes_ChsOnly_NoScopes(t *testing.T) {
	c := Claims{Chs: []string{"private:onlychs.user.7.notifications"}}
	if !c.Authorizes("private:onlychs.user.7.notifications", VerbSubscribe) {
		t.Error("Chs-only token must authorize subscribe")
	}
	if !c.Authorizes("private:onlychs.user.7.notifications", VerbPublish) {
		t.Error("Chs-only token must authorize publish (verb-agnostic)")
	}
	if c.Authorizes("private:onlychs.user.7.somethingelse", VerbSubscribe) {
		t.Error("Chs-only token must deny non-matching names")
	}
	if len(c.Scopes) != 0 {
		t.Fatalf("test sanity: claims should carry zero scopes; got %d", len(c.Scopes))
	}
}

// TestClaims_Authorizes_DenyMalformedPatternFailsClosed — a deny scope
// with a junk pattern cannot accidentally pass-through and authorize
// the channel. The MatchesVerb helper fails closed on compile error,
// so the deny pass yields no match and the allow pass runs normally.
func TestClaims_Authorizes_DenyMalformedPatternFailsClosed(t *testing.T) {
	c := Claims{
		Scopes: []Scope{
			{Pattern: "not a pattern", Verbs: []Verb{VerbSubscribe}, Deny: true},
			{Pattern: "private:tenant.42.**", Verbs: []Verb{VerbSubscribe}},
		},
	}
	if !c.Authorizes("private:tenant.42.alerts", VerbSubscribe) {
		t.Error("malformed deny pattern must fail closed (no deny) so allow proceeds")
	}
}

// TestScope_IsDeny — small accessor check.
func TestScope_IsDeny(t *testing.T) {
	allow := Scope{Pattern: "private:foo.*", Verbs: []Verb{VerbSubscribe}}
	deny := Scope{Pattern: "private:foo.bar", Verbs: []Verb{VerbSubscribe}, Deny: true}
	if allow.IsDeny() {
		t.Error("default scope is not a deny")
	}
	if !deny.IsDeny() {
		t.Error("scope with Deny=true should report IsDeny()")
	}
}

// TestClaims_Authorizes_OrderIndependent — operators can place deny
// scopes anywhere in the Scopes slice and the result is the same.
// Guards against an implementation that accidentally short-circuited
// on the first scope it saw.
func TestClaims_Authorizes_OrderIndependent(t *testing.T) {
	denyFirst := Claims{
		Scopes: []Scope{
			{Pattern: "private:webapp.user.financials.**", Verbs: []Verb{VerbSubscribe}, Deny: true},
			{Pattern: "private:webapp.user.**", Verbs: []Verb{VerbSubscribe}},
		},
	}
	denyLast := Claims{
		Scopes: []Scope{
			{Pattern: "private:webapp.user.**", Verbs: []Verb{VerbSubscribe}},
			{Pattern: "private:webapp.user.financials.**", Verbs: []Verb{VerbSubscribe}, Deny: true},
		},
	}
	for _, c := range []Claims{denyFirst, denyLast} {
		if c.Authorizes("private:webapp.user.financials.q1", VerbSubscribe) {
			t.Error("financials.q1 must be denied regardless of scope order")
		}
		if !c.Authorizes("private:webapp.user.42", VerbSubscribe) {
			t.Error("user.42 must remain authorized regardless of scope order")
		}
	}
}
