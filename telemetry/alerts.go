package telemetry

import (
	"fmt"
	"sort"
)

// Severity classifies an alert firing. The string values are stable and
// surface verbatim in JSON output, Prometheus labels, and operator
// dashboards.
type Severity string

const (
	// SeverityInfo is informational; the condition is unusual but does
	// not require operator action. Use for capacity warnings well
	// below their hard ceiling.
	SeverityInfo Severity = "info"
	// SeverityWarning means the condition warrants investigation. The
	// system is still serving traffic but a metric has crossed a
	// threshold that historically precedes degradation.
	SeverityWarning Severity = "warning"
	// SeverityCritical means immediate action — the condition either
	// already degrades user experience or will within minutes.
	SeverityCritical Severity = "critical"
)

// Valid reports whether s is a recognized severity.
func (s Severity) Valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return true
	}
	return false
}

// AlertRule is a declarative gate on Snapshot fields. The embedder
// supplies a list of rules to the Aggregator; on every Snapshot the
// rules are evaluated and any that return true are surfaced as
// FiringAlert entries in Snapshot.Alerts (and, when the Prometheus
// handler is mounted, exposed as `parsec_alerts_firing{alert,severity}`
// gauges).
//
// The Condition closure receives the Snapshot pre-Alerts, so a rule
// cannot reference its own firing. Side effects in Condition are
// strongly discouraged — the function may be called multiple times
// per scrape window.
type AlertRule struct {
	// Name is a stable identifier used in JSON output, Prometheus
	// labels, and operator runbooks. Must be unique within the rule
	// list; duplicates are rejected by ValidateAlertRules.
	Name string `json:"name"`
	// Severity classifies the firing. See Severity for the levels.
	Severity Severity `json:"severity"`
	// Description is the operator-facing one-liner explaining what the
	// rule guards against. Shown in dashboards next to the firing
	// state.
	Description string `json:"description,omitempty"`
	// Condition returns true when the rule should fire for snap.
	Condition func(snap Snapshot) bool `json:"-"`
}

// FiringAlert is the per-snapshot record of a rule that evaluated true.
// It is the JSON-friendly subset of AlertRule (no Condition closure)
// plus the snapshot timestamp so consumers can correlate firings with
// dashboard time ranges.
type FiringAlert struct {
	Name        string   `json:"name"`
	Severity    Severity `json:"severity"`
	Description string   `json:"description,omitempty"`
}

// ValidateAlertRules checks the rule list for shape problems before the
// Aggregator starts using it. Empty names, unknown severities, nil
// Condition closures, and duplicate names are all rejected. The
// embedder calls this at boot so misconfigured deployments fail loud
// rather than silently dropping firings.
func ValidateAlertRules(rules []AlertRule) error {
	seen := map[string]struct{}{}
	for i, r := range rules {
		if r.Name == "" {
			return fmt.Errorf("telemetry: rule %d has empty Name", i)
		}
		if !r.Severity.Valid() {
			return fmt.Errorf("telemetry: rule %q has invalid Severity %q", r.Name, r.Severity)
		}
		if r.Condition == nil {
			return fmt.Errorf("telemetry: rule %q has nil Condition", r.Name)
		}
		if _, dup := seen[r.Name]; dup {
			return fmt.Errorf("telemetry: rule name %q appears more than once", r.Name)
		}
		seen[r.Name] = struct{}{}
	}
	return nil
}

// EvaluateAlerts walks rules and returns the FiringAlert list for the
// snapshot, sorted by Name for stable ordering. A nil rule list
// returns nil.
func EvaluateAlerts(snap Snapshot, rules []AlertRule) []FiringAlert {
	if len(rules) == 0 {
		return nil
	}
	out := make([]FiringAlert, 0, len(rules))
	for _, r := range rules {
		if r.Condition == nil {
			continue
		}
		if r.Condition(snap) {
			out = append(out, FiringAlert{
				Name:        r.Name,
				Severity:    r.Severity,
				Description: r.Description,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
