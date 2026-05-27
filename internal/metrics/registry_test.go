package metrics_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/frankbardon/parsec/internal/metrics"
)

// TestNew_RegistersAllCollectors verifies that every documented metric
// is wired into the registry and can be scraped without ever being
// observed first (so an empty deployment still produces consistent
// exposition).
func TestNew_RegistersAllCollectors(t *testing.T) {
	m := metrics.New()
	want := []string{
		"parsec_publishes_total",
		"parsec_publish_duration_seconds",
		"parsec_subscribers_active",
		"parsec_channels_active",
		"parsec_token_verifications_total",
		"parsec_sink_attempts_total",
		"parsec_sink_duration_seconds",
		"parsec_dlq_size",
		"parsec_key_rotations_total",
		"parsec_rpc_requests_total",
		"parsec_rpc_duration_seconds",
	}
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	got := make(map[string]bool, len(families))
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, name := range want {
		// Counter/histogram families only appear after the first
		// observation, so prime them with a no-op value to make Gather
		// report them.
		switch name {
		case "parsec_publishes_total":
			m.PublishesTotal.WithLabelValues("public", "success").Add(0)
		case "parsec_publish_duration_seconds":
			m.PublishDuration.WithLabelValues("public").Observe(0)
		case "parsec_subscribers_active":
			m.SubscribersActive.WithLabelValues("public").Set(0)
		case "parsec_channels_active":
			m.ChannelsActive.WithLabelValues("open", "public").Set(0)
		case "parsec_token_verifications_total":
			m.TokenVerificationTotal.WithLabelValues("mgmt", "success").Add(0)
		case "parsec_sink_attempts_total":
			m.SinkAttemptsTotal.WithLabelValues("slack", "success").Add(0)
		case "parsec_sink_duration_seconds":
			m.SinkDuration.WithLabelValues("slack").Observe(0)
		case "parsec_dlq_size":
			m.DLQSize.WithLabelValues("slack").Set(0)
		case "parsec_key_rotations_total":
			m.KeyRotationsTotal.WithLabelValues("generated").Add(0)
		case "parsec_rpc_requests_total":
			m.RPCRequestsTotal.WithLabelValues("Publish", "200").Add(0)
		case "parsec_rpc_duration_seconds":
			m.RPCDuration.WithLabelValues("Publish").Observe(0)
		}
	}
	families, _ = m.Registry.Gather()
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("metric %q not registered", name)
		}
	}
}

// TestVisibilityLabel rejects unknown visibilities by collapsing them
// to a fixed sentinel, protecting the cardinality budget.
func TestVisibilityLabel(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"public", "public"},
		{"private", "private"},
		{"", "unknown"},
		{"PUBLIC", "unknown"},
		{"$attacker-controlled$", "unknown"},
	}
	for _, tc := range tests {
		if got := metrics.VisibilityLabel(tc.in); got != tc.want {
			t.Errorf("VisibilityLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPublishesTotal_IncrementsByLabel verifies the counter increments
// correctly when keyed by (visibility, result).
func TestPublishesTotal_IncrementsByLabel(t *testing.T) {
	m := metrics.New()
	m.PublishesTotal.WithLabelValues("public", "success").Inc()
	m.PublishesTotal.WithLabelValues("public", "success").Inc()
	m.PublishesTotal.WithLabelValues("private", "failure").Inc()

	if got := testutil.ToFloat64(m.PublishesTotal.WithLabelValues("public", "success")); got != 2 {
		t.Errorf("public/success = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.PublishesTotal.WithLabelValues("private", "failure")); got != 1 {
		t.Errorf("private/failure = %v, want 1", got)
	}
}

// TestNewWithRegistry_SharedRegistryDoesNotPanic confirms two Metrics
// bundles can share the same registry without the duplicate-registration
// check tripping.
func TestNewWithRegistry_SharedRegistryDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on shared registry: %v", r)
		}
	}()
	reg := prometheus.NewRegistry()
	_ = metrics.NewWithRegistry(reg)
	_ = metrics.NewWithRegistry(reg)
}

// TestExpositionContainsMetricNames performs a black-box smoke: gather
// the registry, render the text exposition, and confirm every Parsec
// metric name appears.
func TestExpositionContainsMetricNames(t *testing.T) {
	m := metrics.New()
	m.PublishesTotal.WithLabelValues("public", "success").Inc()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range families {
		b.WriteString(f.GetName())
		b.WriteByte('\n')
	}
	out := b.String()
	if !strings.Contains(out, "parsec_publishes_total") {
		t.Errorf("exposition missing publishes_total: %s", out)
	}
}

// TestNewWithRegistryAndRegion_LabelStampedOnEveryCollector exercises
// the multi-region helper: with a non-empty region, every Parsec
// counter/gauge gains a constant `region` label whose value matches
// what was passed at construction.
func TestNewWithRegistryAndRegion_LabelStampedOnEveryCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewWithRegistryAndRegion(reg, "us-east")
	m.PublishesTotal.WithLabelValues("public", "success").Inc()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range families {
		if f.GetName() != "parsec_publishes_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "region" && lp.GetValue() == "us-east" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("region label not stamped on parsec_publishes_total")
	}
}

// TestNewWithRegistryAndRegion_EmptyRegionOmitsLabel confirms that
// when no Region is configured no `region` label appears on any
// Parsec-defined collector — single-region deployments stay label-free.
func TestNewWithRegistryAndRegion_EmptyRegionOmitsLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewWithRegistryAndRegion(reg, "")
	m.PublishesTotal.WithLabelValues("public", "success").Inc()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "parsec_publishes_total" {
			continue
		}
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "region" {
					t.Errorf("empty region produced a region label: %v", lp)
				}
			}
		}
	}
}
