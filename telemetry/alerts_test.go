package telemetry

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateAlertRules(t *testing.T) {
	good := []AlertRule{
		{Name: "a", Severity: SeverityWarning, Condition: func(Snapshot) bool { return false }},
		{Name: "b", Severity: SeverityCritical, Condition: func(Snapshot) bool { return true }},
	}
	if err := ValidateAlertRules(good); err != nil {
		t.Fatalf("good rules failed: %v", err)
	}

	cases := []struct {
		label string
		rules []AlertRule
	}{
		{"empty name", []AlertRule{{Severity: SeverityInfo, Condition: func(Snapshot) bool { return false }}}},
		{"bad severity", []AlertRule{{Name: "x", Severity: "oops", Condition: func(Snapshot) bool { return false }}}},
		{"nil condition", []AlertRule{{Name: "x", Severity: SeverityInfo}}},
		{"dup name", []AlertRule{
			{Name: "x", Severity: SeverityInfo, Condition: func(Snapshot) bool { return false }},
			{Name: "x", Severity: SeverityWarning, Condition: func(Snapshot) bool { return false }},
		}},
	}
	for _, c := range cases {
		if err := ValidateAlertRules(c.rules); err == nil {
			t.Errorf("%s: expected validation error", c.label)
		}
	}
}

func TestEvaluateAlertsFiresAndSorts(t *testing.T) {
	rules := []AlertRule{
		{Name: "zeta", Severity: SeverityWarning, Description: "z", Condition: func(s Snapshot) bool { return s.Tokens.ActiveCount > 0 }},
		{Name: "alpha", Severity: SeverityCritical, Description: "a", Condition: func(s Snapshot) bool { return s.Tokens.ActiveCount > 0 }},
		{Name: "skip", Severity: SeverityInfo, Condition: func(s Snapshot) bool { return false }},
	}
	snap := Snapshot{Tokens: TokenStats{ActiveCount: 5}}
	got := EvaluateAlerts(snap, rules)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("sort order = %v", got)
	}
	if got[0].Severity != SeverityCritical {
		t.Fatalf("severity = %v", got[0].Severity)
	}
}

func TestAggregatorWithAlertsValidatesAndFires(t *testing.T) {
	agg := New(fakeSource{Snapshot{Tokens: TokenStats{ActiveCount: 10}}})

	// Invalid rule short-circuits.
	if _, err := agg.WithAlerts([]AlertRule{{Name: "", Severity: SeverityInfo}}); err == nil {
		t.Fatal("expected WithAlerts to reject empty name")
	}

	agg2, err := agg.WithAlerts([]AlertRule{
		{Name: "tokens_active", Severity: SeverityWarning, Description: "active > 5", Condition: func(s Snapshot) bool {
			return s.Tokens.ActiveCount > 5
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := agg2.Snapshot(context.Background())
	if len(snap.Alerts) != 1 || snap.Alerts[0].Name != "tokens_active" {
		t.Fatalf("alerts = %v", snap.Alerts)
	}
}

func TestPrometheusHandlerRendersGaugesAndAlerts(t *testing.T) {
	agg := New(fakeSource{Snapshot{
		Channels:  ChannelStats{TotalActive: 3, ByPattern: map[string]int64{"public:*": 2, "private:*.id": 1}},
		Envelopes: EnvelopeStats{RatePerSecond: 1.5, ByAspect: map[string]int64{"data": 7}},
		Tokens:    TokenStats{IssuedLastHour: 4, ActiveCount: 10},
		Cache:     CacheStats{HitRatePct: 92.5, SizeEntries: 100},
	}})
	agg, err := agg.WithAlerts([]AlertRule{
		{Name: "cache_cold", Severity: SeverityInfo, Description: "low hit rate", Condition: func(s Snapshot) bool {
			return s.Cache.HitRatePct < 95
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/telemetry/prom", nil)
	agg.PrometheusHandler().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()

	wants := []string{
		"# TYPE parsec_telemetry_channels_active gauge",
		"parsec_telemetry_channels_active 3",
		`parsec_telemetry_channels_by_pattern{pattern="public:*"} 2`,
		`parsec_telemetry_envelopes_by_aspect{aspect="data"} 7`,
		"parsec_telemetry_envelope_rate_per_second 1.5",
		"parsec_telemetry_tokens_active 10",
		"parsec_telemetry_cache_hit_rate_pct 92.5",
		`parsec_telemetry_alerts_firing{alert="cache_cold",severity="info"} 1`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %q\n--- body ---\n%s", w, body)
		}
	}
}

func TestEscapeLabelHandlesQuotesAndBackslashes(t *testing.T) {
	got := escapeLabel(`foo"bar\baz` + "\n")
	want := `foo\"bar\\baz\n`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
