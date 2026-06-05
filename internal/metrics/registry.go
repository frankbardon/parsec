// Package metrics centralizes the Prometheus collectors Parsec exposes.
//
// Cardinality budget: channel names, subject IDs, and bearer tokens are
// NEVER labels. Visibility (public/private), result enums, and bounded
// method/sink names are the only acceptable label values.
//
// Every subsystem that records a metric reaches for a singleton Metrics
// value via New (typically constructed once at parsec.New and wired into
// the subsystem at construction). The package never registers against
// the global prometheus.DefaultRegisterer — callers supply their own
// registry or take the package-default one returned by NewWithRegistry(nil).
package metrics

import (
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// LabelValue constants keep call-site labels consistent and prevent typos
// from blowing the cardinality budget.
const (
	ResultSuccess = "success"
	ResultFailure = "failure"

	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
	VisibilityUnknown = "unknown"

	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
	TokenTypeMgmt    = "mgmt"

	KeyActionGenerated = "generated"
	KeyActionPromoted  = "promoted"
	KeyActionRetired   = "retired"

	RPCNonRPC = "non_rpc"
)

// Metrics is the bundle of Parsec collectors. Exported fields make call
// sites read like prom client code: metrics.PublishesTotal.With(...).Inc().
//
// Construct with New. Each instance owns its own *prometheus.Registry so
// tests can spin up independent metric universes.
type Metrics struct {
	Registry *prometheus.Registry

	PublishesTotal         *prometheus.CounterVec
	PublishDuration        *prometheus.HistogramVec
	SubscribersActive      *prometheus.GaugeVec
	ChannelsActive         *prometheus.GaugeVec
	TokenVerificationTotal *prometheus.CounterVec
	SinkAttemptsTotal      *prometheus.CounterVec
	SinkDuration           *prometheus.HistogramVec
	DLQSize                *prometheus.GaugeVec
	KeyRotationsTotal      *prometheus.CounterVec
	RPCRequestsTotal       *prometheus.CounterVec
	RPCDuration            *prometheus.HistogramVec
	RateLimitDecisions     *prometheus.CounterVec
	RefreshRotations       *prometheus.CounterVec
}

// New constructs a fresh Metrics bundle backed by a fresh registry. The
// registry includes Go runtime + process collectors for free.
func New() *Metrics {
	return NewWithRegistry(nil)
}

// NewWithRegistry constructs a Metrics bundle bound to reg. If reg is nil
// a fresh prometheus.Registry is created.
//
// All collectors are registered eagerly so /metrics output is stable even
// before the first publish. If reg already has any of these collectors
// registered, the duplicate registrations are skipped — useful when
// callers share a registry across subsystems.
func NewWithRegistry(reg *prometheus.Registry) *Metrics {
	return NewWithRegistryAndRegion(reg, "")
}

// NewWithRegistryAndRegion is NewWithRegistry plus a constant region
// label applied to every Parsec-defined collector via
// prometheus.WrapRegistererWith. Pass region="" (the default) to skip
// the label entirely so single-region deployments stay label-free.
// The Go/process collectors are NOT region-labeled — they describe the
// host, not Parsec semantics.
func NewWithRegistryAndRegion(reg *prometheus.Registry, region string) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
		// Include Go + process collectors so /metrics is useful out of
		// the box. Registration errors here mean the registry was
		// pre-populated; ignore.
		_ = reg.Register(collectors.NewGoCollector())
		_ = reg.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	}
	// regRegisterer is what we register Parsec collectors against. When a
	// region is configured we wrap the underlying registry so every
	// collector gets a constant `region` label without each call site
	// threading the value.
	var regRegisterer prometheus.Registerer = reg
	if region != "" {
		regRegisterer = prometheus.WrapRegistererWith(prometheus.Labels{"region": region}, reg)
	}
	m := &Metrics{
		Registry: reg,
		PublishesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "parsec_publishes_total",
			Help: "Total publishes by channel visibility and result.",
		}, []string{"channel_visibility", "result"}),
		PublishDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "parsec_publish_duration_seconds",
			Help:    "Publish latency in seconds, observed at the broker boundary.",
			Buckets: prometheus.DefBuckets,
		}, []string{"channel_visibility"}),
		SubscribersActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "parsec_subscribers_active",
			Help: "Active subscriber count by channel visibility.",
		}, []string{"channel_visibility"}),
		ChannelsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "parsec_channels_active",
			Help: "Channels currently tracked by the manager, by state and visibility.",
		}, []string{"state", "visibility"}),
		TokenVerificationTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "parsec_token_verifications_total",
			Help: "Token verifications by type and result.",
		}, []string{"type", "result"}),
		SinkAttemptsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "parsec_sink_attempts_total",
			Help: "Sink send attempts by sink name and result.",
		}, []string{"sink", "result"}),
		SinkDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "parsec_sink_duration_seconds",
			Help:    "Sink send duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"sink"}),
		DLQSize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "parsec_dlq_size",
			Help: "Current dead-letter queue depth by sink name. Updated by periodic scrape callback.",
		}, []string{"sink"}),
		KeyRotationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "parsec_key_rotations_total",
			Help: "Key-ring mutations by action (generated, promoted, retired).",
		}, []string{"action"}),
		RPCRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "parsec_rpc_requests_total",
			Help: "RPC requests by method (last URL segment) and HTTP status.",
		}, []string{"method", "status"}),
		RPCDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "parsec_rpc_duration_seconds",
			Help:    "RPC duration in seconds by method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		RateLimitDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "parsec_rate_limit_decisions_total",
			Help: "Rate-limit decisions by bucket (publish/subscribe/token-issue) and result (allowed/denied).",
		}, []string{"bucket", "result"}),
		RefreshRotations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "parsec_refresh_rotations_total",
			Help: "Refresh-token rotation outcomes (rotated, reused, family_revoked, legacy).",
		}, []string{"result"}),
	}
	for _, c := range []prometheus.Collector{
		m.PublishesTotal, m.PublishDuration, m.SubscribersActive, m.ChannelsActive,
		m.TokenVerificationTotal, m.SinkAttemptsTotal, m.SinkDuration, m.DLQSize,
		m.KeyRotationsTotal, m.RPCRequestsTotal, m.RPCDuration,
		m.RateLimitDecisions, m.RefreshRotations,
	} {
		// Skip duplicate registrations — callers may share a registry
		// across two Metrics bundles in tests.
		if err := regRegisterer.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if !errors.As(err, &are) {
				// Programming error; panic so tests fail loud.
				panic(err)
			}
		}
	}
	return m
}

// VisibilityLabel maps a channels.Visibility-typed string to a stable
// label value. Falls back to VisibilityUnknown for unrecognized inputs
// so we never accept an unbounded user-controlled label.
func VisibilityLabel(v string) string {
	switch v {
	case VisibilityPublic, VisibilityPrivate:
		return v
	default:
		return VisibilityUnknown
	}
}

// Noop returns a Metrics instance bound to a private registry that no
// caller can scrape. Useful in tests where you want call sites to no-op
// without nil checks.
var (
	noopOnce sync.Once
	noopVal  *Metrics
)

// Noop returns a process-wide no-op Metrics value. It still registers
// collectors so increments don't panic; the registry is just unreachable.
func Noop() *Metrics {
	noopOnce.Do(func() {
		noopVal = NewWithRegistry(prometheus.NewRegistry())
	})
	return noopVal
}
