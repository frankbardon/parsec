package auth

import (
	"testing"

	"github.com/frankbardon/parsec/channels"
)

func TestVerb_Valid(t *testing.T) {
	for _, v := range AllVerbs {
		if !v.Valid() {
			t.Errorf("expected %q valid", v)
		}
	}
	if Verb("delete").Valid() {
		t.Error("unknown verb should be invalid")
	}
}

func TestScope_Compiled_Caches(t *testing.T) {
	s := &Scope{Pattern: "private:foo.*", Verbs: []Verb{VerbSubscribe}}
	p1, err := s.Compiled()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.Compiled()
	if err != nil {
		t.Fatal(err)
	}
	if p1.String() != p2.String() {
		t.Error("cached pattern should be identical across calls")
	}
}

func TestScope_Compiled_ErrorSticky(t *testing.T) {
	s := &Scope{Pattern: "*:foo.bar", Verbs: []Verb{VerbSubscribe}}
	_, err := s.Compiled()
	if err == nil {
		t.Fatal("expected compile error on cross-visibility pattern")
	}
	// Second call should return the same error without re-parsing.
	_, err2 := s.Compiled()
	if err2 == nil {
		t.Fatal("expected sticky compile error on second call")
	}
}

func TestScope_Authorizes_VerbCheck(t *testing.T) {
	s := &Scope{Pattern: "private:foo.bar.*", Verbs: []Verb{VerbSubscribe}}
	n, _ := channels.ParseName("private:foo.bar.7")
	if !s.Authorizes(n, VerbSubscribe) {
		t.Error("subscribe should be allowed")
	}
	if s.Authorizes(n, VerbPublish) {
		t.Error("publish not in verb list — should be denied")
	}
	if s.Authorizes(n, VerbManage) {
		t.Error("manage not in verb list — should be denied")
	}
}

func TestScope_Authorizes_PatternMismatchDenies(t *testing.T) {
	s := &Scope{Pattern: "private:foo.bar.*", Verbs: []Verb{VerbSubscribe, VerbPublish}}
	mismatch, _ := channels.ParseName("private:foo.baz.7")
	if s.Authorizes(mismatch, VerbSubscribe) {
		t.Error("non-matching pattern must deny even with full verb list")
	}
}

func TestScope_Authorizes_MalformedPatternFailsClosed(t *testing.T) {
	s := &Scope{Pattern: "not a pattern", Verbs: []Verb{VerbSubscribe}}
	n, _ := channels.ParseName("private:foo.bar.7")
	if s.Authorizes(n, VerbSubscribe) {
		t.Error("malformed pattern must fail closed")
	}
}
