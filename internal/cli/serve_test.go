package cli_test

import (
	"testing"
	"time"

	"github.com/frankbardon/parsec/internal/cli"
)

func TestParseRateSpec_EmptyIsUnlimited(t *testing.T) {
	l, err := cli.ParseRateSpec("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Unlimited() {
		t.Fatal("empty spec should be unlimited")
	}
}

func TestParseRateSpec_HappyPaths(t *testing.T) {
	cases := []struct {
		spec      string
		burst     int
		wantRate  int
		wantPer   time.Duration
		wantBurst int
	}{
		{"100/s", 50, 100, time.Second, 100}, // burst <= rate is clamped up
		{"100/s", 200, 100, time.Second, 200},
		{"5/m", 0, 5, time.Minute, 5},
		{"1/h", 0, 1, time.Hour, 1},
		{"30/500ms", 0, 30, 500 * time.Millisecond, 30},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			l, err := cli.ParseRateSpec(c.spec, c.burst)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			l = l.Normalize()
			if l.Rate != c.wantRate {
				t.Errorf("Rate = %d, want %d", l.Rate, c.wantRate)
			}
			if l.Per != c.wantPer {
				t.Errorf("Per = %v, want %v", l.Per, c.wantPer)
			}
			if l.Burst != c.wantBurst {
				t.Errorf("Burst = %d, want %d", l.Burst, c.wantBurst)
			}
		})
	}
}

func TestParseRateSpec_ErrorCases(t *testing.T) {
	cases := []string{
		"abc",       // missing /
		"abc/s",     // non-integer rate
		"10/wat",    // bad duration
		"10/0s",     // zero window
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := cli.ParseRateSpec(spec, 0); err == nil {
				t.Fatalf("expected error for %q", spec)
			}
		})
	}
}

func TestParseRateLimits_AllThreeBuckets(t *testing.T) {
	rl, err := cli.ParseRateLimits("100/s", 30, "20/s", 0, "5/m", 10)
	if err != nil {
		t.Fatal(err)
	}
	if rl.Publish.Rate != 100 || rl.Publish.Burst != 30 {
		t.Errorf("Publish = %+v", rl.Publish)
	}
	if rl.Subscribe.Rate != 20 {
		t.Errorf("Subscribe = %+v", rl.Subscribe)
	}
	if rl.TokenIssue.Rate != 5 || rl.TokenIssue.Per != time.Minute {
		t.Errorf("TokenIssue = %+v", rl.TokenIssue)
	}
}

func TestParseRateLimits_EmptyZero(t *testing.T) {
	rl, err := cli.ParseRateLimits("", 0, "", 0, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !rl.Empty() {
		t.Fatalf("all-empty should be Empty(): %+v", rl)
	}
}
