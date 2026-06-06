package telemetry

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// PrometheusHandler returns an http.Handler that writes the Snapshot as
// Prometheus text exposition (version 0.0.4). The handler re-aggregates
// on every scrape so the values are always current.
//
// Mount it under a path the operator's Prometheus is scraping — for
// example `/parsec/telemetry/metrics` (kept separate from the existing
// `/metrics` collector registry to avoid metric-name collisions). The
// embedder is responsible for the mount; this package only returns the
// handler.
//
// Cardinality budget: only bounded label sets escape — `pattern` is a
// configured channel grammar pattern, `aspect` is the envelope kind
// supplied by the embedder, `alert` is the rule name, `severity` is one
// of info/warning/critical. Operators who plumb a high-cardinality
// pattern or aspect into a Source are responsible for the bytes — the
// handler does not truncate.
func (a *Aggregator) PrometheusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := a.Snapshot(r.Context())
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		writeSnapshotProm(w, snap)
	})
}

// writeSnapshotProm renders snap in Prometheus text exposition format.
// Exposed for tests.
func writeSnapshotProm(w io.Writer, snap Snapshot) {
	writeGauge(w, "parsec_telemetry_channels_active", "Active channels reported by every telemetry source, summed.", float64(snap.Channels.TotalActive), nil)
	if len(snap.Channels.ByPattern) > 0 {
		writeGaugeHeader(w, "parsec_telemetry_channels_by_pattern", "Active channels per configured pattern.")
		for _, k := range sortedKeys(snap.Channels.ByPattern) {
			writeGaugeLine(w, "parsec_telemetry_channels_by_pattern", float64(snap.Channels.ByPattern[k]), map[string]string{"pattern": k})
		}
	}

	writeGauge(w, "parsec_telemetry_envelope_rate_per_second", "Envelope publish rate averaged across telemetry sources.", snap.Envelopes.RatePerSecond, nil)
	if len(snap.Envelopes.ByAspect) > 0 {
		writeGaugeHeader(w, "parsec_telemetry_envelopes_by_aspect", "Cumulative envelope count per aspect.")
		for _, k := range sortedKeys(snap.Envelopes.ByAspect) {
			writeGaugeLine(w, "parsec_telemetry_envelopes_by_aspect", float64(snap.Envelopes.ByAspect[k]), map[string]string{"aspect": k})
		}
	}

	writeGauge(w, "parsec_telemetry_presence_total_users", "Total distinct user presence entries across sources.", float64(snap.Presence.TotalUsers), nil)
	writeGauge(w, "parsec_telemetry_presence_total_agents", "Total distinct agent presence entries across sources.", float64(snap.Presence.TotalAgents), nil)
	writeGauge(w, "parsec_telemetry_presence_average_per_channel", "Average presence count per channel.", snap.Presence.AveragePerChannel, nil)

	writeGauge(w, "parsec_telemetry_history_buffered", "Envelopes currently held in channel history buffers.", float64(snap.History.TotalEnvelopesBuffered), nil)
	writeGauge(w, "parsec_telemetry_history_buffer_utilization_pct", "History buffer utilization, percent of configured capacity.", snap.History.BufferUtilizationPct, nil)

	writeGauge(w, "parsec_telemetry_tokens_issued_last_hour", "Tokens issued in the last hour across sources.", float64(snap.Tokens.IssuedLastHour), nil)
	writeGauge(w, "parsec_telemetry_tokens_revoked_last_hour", "Tokens revoked in the last hour across sources.", float64(snap.Tokens.RevokedLastHour), nil)
	writeGauge(w, "parsec_telemetry_tokens_active", "Tokens currently considered active across sources.", float64(snap.Tokens.ActiveCount), nil)

	writeGauge(w, "parsec_telemetry_cache_hit_rate_pct", "Cache hit rate as a percent across sources.", snap.Cache.HitRatePct, nil)
	writeGauge(w, "parsec_telemetry_cache_size_bytes", "Cache size in bytes summed across sources.", float64(snap.Cache.SizeBytes), nil)
	writeGauge(w, "parsec_telemetry_cache_size_entries", "Cache entry count summed across sources.", float64(snap.Cache.SizeEntries), nil)
	writeGauge(w, "parsec_telemetry_cache_evictions_last_hour", "Cache evictions in the last hour across sources.", float64(snap.Cache.EvictionsLastHour), nil)

	if len(snap.Alerts) > 0 {
		writeGaugeHeader(w, "parsec_telemetry_alerts_firing", "Alert rules currently firing. 1 = firing.")
		for _, fa := range snap.Alerts {
			writeGaugeLine(w, "parsec_telemetry_alerts_firing", 1, map[string]string{
				"alert":    fa.Name,
				"severity": string(fa.Severity),
			})
		}
	}
}

// writeGauge emits a HELP+TYPE preamble and one sample line.
func writeGauge(w io.Writer, name, help string, value float64, labels map[string]string) {
	writeGaugeHeader(w, name, help)
	writeGaugeLine(w, name, value, labels)
}

// writeGaugeHeader writes the HELP+TYPE preamble for name.
func writeGaugeHeader(w io.Writer, name, help string) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s gauge\n", name)
}

// writeGaugeLine writes one labeled sample of name.
func writeGaugeLine(w io.Writer, name string, value float64, labels map[string]string) {
	if len(labels) == 0 {
		fmt.Fprintf(w, "%s %s\n", name, formatFloat(value))
		return
	}
	fmt.Fprintf(w, "%s{%s} %s\n", name, encodeLabels(labels), formatFloat(value))
}

// encodeLabels renders labels in stable key-sorted order with escaped values.
func encodeLabels(labels map[string]string) string {
	keys := sortedKeysStr(labels)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteByte('"')
		b.WriteString(escapeLabel(labels[k]))
		b.WriteByte('"')
	}
	return b.String()
}

// escapeLabel applies the Prometheus exposition escape rules:
// backslash, double-quote, and newline are escaped.
func escapeLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// formatFloat renders f without trailing zeros.
func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", f), "0"), ".")
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysStr(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

