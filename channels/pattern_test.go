package channels

import (
	"strings"
	"testing"
)

// mustParseName is a tiny helper for the truth-table tests.
func mustParseName(t *testing.T, s string) Name {
	t.Helper()
	n, err := ParseName(s)
	if err != nil {
		t.Fatalf("ParseName(%q): %v", s, err)
	}
	return n
}

func TestParsePattern_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		in     string
		reason string
	}{
		{"", "empty"},
		{":foo.bar", "no visibility"},
		{"public:", "empty body"},
		{"*:foo.bar", "cross-visibility wildcard"},
		{"secret:foo.bar", "unknown visibility"},
		{"public:foo:bar.baz", "extra colon"},
		{"public:foo..bar", "empty segment"},
		{"public:foo.**.bar", "double-star not trailing"},
		{"public:FOO.bar", "uppercase literal segment"},
		{"public:foo.bar.baz.qux.quux.corge.grault.garply.waldo", "depth > 8"},
	}
	for _, c := range cases {
		if _, err := ParsePattern(c.in); err == nil {
			t.Errorf("ParsePattern(%q) accepted; expected error (%s)", c.in, c.reason)
		}
	}
}

func TestParsePattern_AcceptsValidShapes(t *testing.T) {
	cases := []string{
		"public:foo.bar",
		"private:foo.bar.7",
		"private:foo.bar.*",
		"private:foo.*.7",
		"private:*.bar.7",
		"private:foo.bar.**",
		"private:foo.bar.7.**",
	}
	for _, in := range cases {
		p, err := ParsePattern(in)
		if err != nil {
			t.Errorf("ParsePattern(%q): %v", in, err)
			continue
		}
		if p.String() != in {
			t.Errorf("round-trip mismatch: got %q want %q", p.String(), in)
		}
	}
}

func TestPattern_ExactMatch(t *testing.T) {
	p, err := ParsePattern("private:foo.bar.7")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Matches(mustParseName(t, "private:foo.bar.7")) {
		t.Error("exact pattern should match itself")
	}
	if p.Matches(mustParseName(t, "private:foo.bar.8")) {
		t.Error("exact pattern should not match a different id")
	}
}

func TestPattern_SingleStar(t *testing.T) {
	p, err := ParsePattern("private:foo.bar.*")
	if err != nil {
		t.Fatal(err)
	}
	// Single-star matches exactly one segment.
	if !p.Matches(mustParseName(t, "private:foo.bar.7")) {
		t.Error("single-star should match a single id segment")
	}
	if !p.Matches(mustParseName(t, "private:foo.bar.baz")) {
		t.Error("single-star should match any literal id segment")
	}
	// Single-star does not match across multiple segments.
	if p.Matches(mustParseName(t, "private:foo.bar.7.x")) {
		t.Error("single-star should not match a name with an extra trailing segment")
	}
	// Single-star requires the wildcard segment to be present.
	// Pattern has 2 segments (foo + *) but the synthesized name has only 1.
	oneSeg := Name{Visibility: VisibilityPrivate, App: "foo"}
	p2, _ := ParsePattern("private:foo.*")
	if p2.Matches(oneSeg) {
		t.Error("single-star at index 1 should not match a 1-segment name")
	}
}

func TestPattern_DoubleStar(t *testing.T) {
	p, err := ParsePattern("private:foo.bar.**")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Matches(mustParseName(t, "private:foo.bar.7")) {
		t.Error("double-star should match one trailing segment")
	}
	if !p.Matches(mustParseName(t, "private:foo.bar.7.x")) {
		t.Error("double-star should match two trailing segments")
	}
	// A Name is at most 4 segments via ParseName, but the matcher does
	// not depend on that — test the boundary via a custom Name.
	deeper := Name{Visibility: VisibilityPrivate, App: "foo", Domain: "bar", ID: "7", Topic: "x"}
	if !p.Matches(deeper) {
		t.Error("double-star should match a 4-segment name")
	}
	// Double-star requires at least one trailing segment.
	if p.Matches(mustParseName(t, "private:foo.bar.7")) == false {
		// already covered above; this sanity-line keeps the truth table dense
	}
	noTrail := Name{Visibility: VisibilityPrivate, App: "foo", Domain: "bar"}
	if p.Matches(noTrail) {
		t.Error("double-star should not match a name without the required trailing segment")
	}
}

func TestPattern_VisibilityMismatch(t *testing.T) {
	p, err := ParsePattern("private:foo.bar.*")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParseName("public:foo.bar")
	if err != nil {
		t.Fatal(err)
	}
	if p.Matches(pub) {
		t.Error("private pattern must not match a public channel")
	}
}

func TestPattern_SegmentCountMismatch(t *testing.T) {
	p, err := ParsePattern("private:foo.*")
	if err != nil {
		t.Fatal(err)
	}
	// `private:foo` is not a legal channel name (private requires id);
	// build it directly to test segment-count semantics in the matcher.
	short := Name{Visibility: VisibilityPrivate, App: "foo"}
	if p.Matches(short) {
		t.Error("pattern with one wildcard must not match a name missing that segment")
	}
}

func TestPattern_DepthCapRejected(t *testing.T) {
	// 9 segments — one over the cap.
	nine := "private:a.b.c.d.e.f.g.h.i"
	_, err := ParsePattern(nine)
	if err == nil {
		t.Fatalf("expected depth-cap rejection for %q", nine)
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("expected depth-related error, got %v", err)
	}
}

func TestPattern_CrossVisibilityRejected(t *testing.T) {
	// Cross-visibility pattern is the canonical "must reject at parse time" case.
	if _, err := ParsePattern("*:foo.bar"); err == nil {
		t.Error("cross-visibility pattern must be rejected at parse")
	}
}

// TestPattern_IsValid covers the zero value.
func TestPattern_IsValidZeroValue(t *testing.T) {
	var z Pattern
	if z.IsValid() {
		t.Error("zero Pattern should not be valid")
	}
	if z.Matches(mustParseName(t, "private:foo.bar.7")) {
		t.Error("zero Pattern must not match anything")
	}
}
