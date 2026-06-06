package ratelimit

import (
	"testing"
	"time"

	"github.com/frankbardon/parsec/channels"
)

func TestCompileChannelRulesSpecificityOrder(t *testing.T) {
	rules, err := CompileChannelRules(map[string]Limit{
		"public:**":               {Rate: 100, Per: time.Second},
		"public:hot.*":            {Rate: 50, Per: time.Second},
		"public:hot.metrics.feed": {Rate: 10, Per: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("got %d rules", len(rules))
	}
	wantOrder := []string{
		"public:hot.metrics.feed",
		"public:hot.*",
		"public:**",
	}
	for i, want := range wantOrder {
		if rules[i].Raw != want {
			t.Fatalf("rule %d = %q, want %q", i, rules[i].Raw, want)
		}
	}
}

func TestCompileChannelRulesRejectsInvalid(t *testing.T) {
	_, err := CompileChannelRules(map[string]Limit{
		"not a pattern": {Rate: 1, Per: time.Second},
	})
	if err == nil {
		t.Fatal("expected error on bad pattern")
	}
}

func TestRateLimitsMatchPublishPicksMostSpecific(t *testing.T) {
	rules, err := CompileChannelRules(map[string]Limit{
		"public:hot.**":           {Rate: 100, Per: time.Second},
		"public:hot.metrics.feed": {Rate: 5, Per: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	rl := RateLimits{
		Publish:           Limit{Rate: 1000, Per: time.Second},
		PerChannelPublish: rules,
	}.Normalize()

	name, err := channels.ParseName("public:hot.metrics.feed")
	if err != nil {
		t.Fatal(err)
	}
	got, raw := rl.MatchPublish(name)
	if got.Rate != 5 {
		t.Fatalf("rate=%d, want 5 (specific rule)", got.Rate)
	}
	if raw != "public:hot.metrics.feed" {
		t.Fatalf("raw=%q", raw)
	}

	// Non-matching channel falls back to the default Publish.
	name2, _ := channels.ParseName("public:cold.audit.feed")
	got2, raw2 := rl.MatchPublish(name2)
	if got2.Rate != 1000 {
		t.Fatalf("default rate=%d, want 1000", got2.Rate)
	}
	if raw2 != "" {
		t.Fatalf("raw=%q, want empty", raw2)
	}
}

func TestRateLimitsEmptyHonorsPerChannel(t *testing.T) {
	rules, err := CompileChannelRules(map[string]Limit{
		"public:hot.**": {Rate: 5, Per: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	rl := RateLimits{PerChannelPublish: rules}
	if rl.Empty() {
		t.Fatal("RateLimits with active per-channel rule reported Empty=true")
	}
}
