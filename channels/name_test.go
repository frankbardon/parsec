package channels

import "testing"

func TestParseName_Public(t *testing.T) {
	n, err := ParseName("public:webapp.system.status")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !(n.Visibility == VisibilityPublic && n.App == "webapp" && n.Domain == "system" && n.ID == "status" && n.Topic == "") {
		t.Errorf("unexpected parse: %+v", n)
	}
	if got := n.String(); got != "public:webapp.system.status" {
		t.Errorf("round-trip mismatch: %s", got)
	}
}

func TestParseName_PrivateRequiresID(t *testing.T) {
	if _, err := ParseName("private:webapp.session"); err == nil {
		t.Fatal("expected private without id to fail")
	}
}

func TestParseName_PrivateFull(t *testing.T) {
	n, err := ParseName("private:agent.analysis.abc123.progress")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !n.IsPrivate() {
		t.Error("expected private")
	}
	if n.Topic != "progress" {
		t.Errorf("topic = %q", n.Topic)
	}
}

func TestParseName_RejectsUnknownVisibility(t *testing.T) {
	if _, err := ParseName("secret:webapp.x.y"); err == nil {
		t.Error("expected unknown visibility to fail")
	}
}

func TestParseName_RejectsExtraColon(t *testing.T) {
	if _, err := ParseName("public:webapp:session.a"); err == nil {
		t.Error("expected extra colon to fail")
	}
}

func TestParseName_RejectsBadChars(t *testing.T) {
	cases := []string{
		"public:Webapp.system.x", // uppercase
		"public:web app.sys.x",   // space
		"public:webapp..x",       // empty middle
		"public:webapp.sys.x.y.z",
		"public:",
		"",
	}
	for _, s := range cases {
		if _, err := ParseName(s); err == nil {
			t.Errorf("expected %q to fail", s)
		}
	}
}

func TestBuildName(t *testing.T) {
	n, err := BuildName(VisibilityPrivate, "webapp", "user", "42", "downloads")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if n.String() != "private:webapp.user.42.downloads" {
		t.Errorf("string: %s", n)
	}
}
