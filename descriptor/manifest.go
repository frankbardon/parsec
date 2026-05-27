// Package descriptor provides the self-describing manifest envelope used by
// every surface (CLI --json, Twirp Manifest RPC). The envelope
// is the wire-stable shape that consumers parse to learn what this Parsec
// instance can do.
package descriptor

import (
	"encoding/json"
	"io"
	"time"

	"github.com/frankbardon/parsec/ratelimit"
)

// FormatVersion is bumped on any wire-shape change to Envelope or Manifest.
const FormatVersion = "1.0"

// Envelope wraps a typed payload with format and timestamp metadata. It is
// what every surface returns at the top level.
type Envelope struct {
	FormatVersion string    `json:"format_version"`
	Kind          string    `json:"kind"`
	GeneratedAt   time.Time `json:"generated_at"`
	Payload       any       `json:"payload"`
}

// NewEnvelope wraps payload with the standard fields.
func NewEnvelope(kind string, payload any) Envelope {
	return Envelope{
		FormatVersion: FormatVersion,
		Kind:          kind,
		GeneratedAt:   time.Now().UTC(),
		Payload:       payload,
	}
}

// WriteEnvelope marshals env to w as JSON with a trailing newline.
func WriteEnvelope(w io.Writer, env Envelope) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// Manifest describes the Parsec instance: version, sinks, channel namespace
// convention, and key rotation state. Consumed by clients to discover what
// is available without trial-and-error RPC calls.
type Manifest struct {
	Service       string   `json:"service"`
	Version       string   `json:"version"`
	GoVersion     string   `json:"go_version,omitempty"`
	Surfaces      []string `json:"surfaces"`
	Sinks         []string `json:"sinks"`
	Visibilities  []string `json:"visibilities"`
	MaxPrivateTTL string   `json:"max_private_ttl"`
	KeyRotation   string   `json:"key_rotation"`
	ActiveKeyID   string   `json:"active_key_id,omitempty"`
	// Persistence reports the channel-registry backend: "in-memory"
	// (single-node) or "redis" (multi-node). Channel records are
	// ephemeral in either case — clients must reopen public channels
	// and reissue private credentials after a parsec restart.
	Persistence string `json:"persistence"`

	// DLQBackend is the dead-letter queue backend: "memory" or "redis".
	DLQBackend string `json:"dlq_backend,omitempty"`

	// SinkRetry summarises the retry policy. Omitted when retries are
	// disabled.
	SinkRetry *SinkRetrySummary `json:"sink_retry,omitempty"`

	// RateLimits exposes the configured per-bucket budgets. Per-token
	// overrides are private to the issued token and never surface here.
	// Omitted when no buckets are configured.
	RateLimits *ratelimit.RateLimits `json:"rate_limits,omitempty"`

	// MetricsEnabled is true when a Prometheus registry is wired.
	MetricsEnabled bool `json:"metrics_enabled"`

	// TracingEnabled is true when an OTLP endpoint is configured.
	TracingEnabled bool `json:"tracing_enabled"`

	// MetricsPath is the Prometheus scrape endpoint (default /metrics).
	// Omitted when metrics are disabled.
	MetricsPath string `json:"metrics_path,omitempty"`

	// ScopeGrammar describes the channel-name pattern grammar used by the
	// Scopes claim. Client SDKs read this to construct valid patterns at
	// issue time without hard-coding the grammar.
	ScopeGrammar string `json:"scope_grammar,omitempty"`

	// SupportedVerbs lists the verb tokens that may appear in a Scope's
	// verb list. Wire-stable: a new verb is a manifest bump and an
	// explicit code path in the matcher (see CLAUDE.md update demand).
	SupportedVerbs []string `json:"supported_verbs,omitempty"`

	// DenySupported reports whether the issuer accepts deny scopes
	// (Scope.Deny=true). Always true today; declared explicitly so
	// clients can branch on it without parsing scope_precedence.
	DenySupported bool `json:"deny_supported,omitempty"`

	// ScopePrecedence documents the evaluation order applied by
	// Claims.Authorizes. Currently "deny-wins": any matching deny scope
	// rejects regardless of overlapping allow scopes or Chs entries.
	// Wire-stable; future evaluators bump this string.
	ScopePrecedence string `json:"scope_precedence,omitempty"`

	// ConfigSource is the path the operator passed to `parsec serve --config`.
	// Empty when configuration is flag/env only.
	ConfigSource string `json:"config_source,omitempty"`

	// DeltaCompression declares whether per-channel fossil-delta encoding
	// is supported. Currently always "supported".
	DeltaCompression string `json:"delta_compression,omitempty"`

	// Transports lists the wire transports the broker is currently
	// accepting connections on. Always includes "websocket"; includes
	// "webtransport" when the HTTP/3 listener is mounted.
	Transports []string `json:"transports,omitempty"`

	// Region is the operator-configured region label, e.g. "us-east".
	// Empty when single-region mode (no Options.Region) is active.
	Region string `json:"region,omitempty"`

	// Peers is the operator-declared list of peer region URLs. Purely
	// informational — Parsec does no active replication against them.
	// Tools like parsec-keys-sync read this to discover topology.
	Peers []string `json:"peers,omitempty"`

	// AdminUIEnabled reports whether the embedded operator admin SPA is
	// mounted at /admin. Off by default; flipped on via the
	// server.admin_ui.enabled YAML key or the --admin-ui CLI flag.
	AdminUIEnabled bool `json:"admin_ui_enabled"`

	// AuthOIDCEnabled reports whether the OIDC bridge is wired —
	// when true, the management RPC accepts ID tokens from
	// AuthOIDCIssuer in addition to HMAC mgmt tokens. Default false.
	AuthOIDCEnabled bool `json:"auth_oidc_enabled"`

	// AuthOIDCIssuer is the configured OIDC issuer URL when the
	// bridge is enabled, "" otherwise. Lets operators discover the
	// right `parsec login oidc` invocation from the manifest.
	AuthOIDCIssuer string `json:"auth_oidc_issuer,omitempty"`
}

// SinkRetrySummary is the manifest snapshot of the sink retry config.
type SinkRetrySummary struct {
	MaxAttempts int    `json:"max_attempts"`
	BaseBackoff string `json:"base_backoff"`
	MaxBackoff  string `json:"max_backoff"`
}
