package auth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestIssuer_PairWithScopes_DenyRoundTrip mints a token whose scopes
// include a deny entry, verifies the signature, and asserts the
// resulting Claims behave per the deny-wins precedence after the JSON
// round-trip. This is the integration check that the wire shape
// preserves Scope.Deny across the issuer → signer → verifier path.
func TestIssuer_PairWithScopes_DenyRoundTrip(t *testing.T) {
	iss, ver := newTestIssuer(t)
	scopes := []Scope{
		{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbSubscribe}},
		{Pattern: "private:webapp.user.42.financials", Verbs: []Verb{VerbSubscribe}, Deny: true},
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
		t.Error("non-denied channel under allow scope must subscribe")
	}
	if ac.Authorizes("private:webapp.user.42.financials", VerbSubscribe) {
		t.Error("denied channel must not subscribe even though allow scope matches")
	}
	if len(ac.Scopes) != 2 {
		t.Fatalf("round-trip should preserve both scopes; got %d", len(ac.Scopes))
	}
	// Find the deny scope post-decode and confirm the Deny flag survived.
	var sawDeny bool
	for i := range ac.Scopes {
		if ac.Scopes[i].Deny {
			sawDeny = true
			if !strings.Contains(ac.Scopes[i].Pattern, "financials") {
				t.Errorf("deny scope pattern lost: %q", ac.Scopes[i].Pattern)
			}
		}
	}
	if !sawDeny {
		t.Error("decoded Claims should contain exactly one deny scope")
	}
}

// TestIssuer_PairWithScopes_DenyJSONShape asserts the wire shape: the
// deny flag is rendered with the json key "deny" and is OMITTED when
// false, so allow-only tokens stay compact on the wire. Uses pointer
// receivers because Scope contains a sync.Mutex (go vet rejects copying
// value receivers into json.Marshal).
func TestIssuer_PairWithScopes_DenyJSONShape(t *testing.T) {
	allow := &Scope{Pattern: "private:foo.bar.*", Verbs: []Verb{VerbSubscribe}}
	deny := &Scope{Pattern: "private:foo.bar.baz", Verbs: []Verb{VerbSubscribe}, Deny: true}

	allowBytes, err := json.Marshal(allow)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(allowBytes), "deny") {
		t.Errorf("allow scope must omit deny field; got %s", string(allowBytes))
	}
	denyBytes, err := json.Marshal(deny)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(denyBytes), `"deny":true`) {
		t.Errorf("deny scope must render \"deny\":true; got %s", string(denyBytes))
	}

	// Decode an allow-shaped scope (no deny key) and confirm Deny=false.
	var s Scope
	if err := json.Unmarshal([]byte(`{"pat":"private:foo.bar.*","v":["subscribe"]}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Deny {
		t.Error("scope JSON without deny key must decode to Deny=false")
	}
}

// TestIssuer_DenyOnly_StillFailsClosed — a token whose ONLY scope is a
// deny (no allow, no Chs) authorizes nothing. The deny is defensive,
// not a grant, and the absence of any allow source means every
// authorize call returns false.
func TestIssuer_DenyOnly_StillFailsClosed(t *testing.T) {
	iss, ver := newTestIssuer(t)
	scopes := []Scope{
		{Pattern: "private:webapp.user.42.**", Verbs: []Verb{VerbSubscribe}, Deny: true},
	}
	// Issuer requires a Chs anchor; the channel itself is unrelated to the
	// deny pattern so we can also assert the Chs path still authorizes it.
	pair, err := iss.IssuePair("user-1", "private:webapp.user.42.profile", 30*time.Minute, scopes)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := ver.Verify(pair.AccessToken, TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	// The bound channel matches the deny pattern → deny wins, even over Chs.
	if ac.Authorizes("private:webapp.user.42.profile", VerbSubscribe) {
		t.Error("deny scope must beat the Chs anchor when both match the request")
	}
	// Something outside the deny pattern has no allow source → still false.
	if ac.Authorizes("private:webapp.user.43.profile", VerbSubscribe) {
		t.Error("channel without any allow source must not authorize")
	}
}
