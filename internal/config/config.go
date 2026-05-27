// Package config defines the on-disk configuration schema for `parsec
// serve`. The format is YAML; the loader supports ${ENV_VAR}
// interpolation on string fields so secrets can be sourced from the
// environment without inlining them.
//
// Precedence between sources is:
//
//	CLI flag > env var > config file > built-in default
//
// CLI and env handling stay in internal/cli; this package only defines
// the file schema and the translation to parsec.Options.
package config

// Config is the top-level schema. Every field is optional; missing
// fields fall through to defaults applied by ApplyDefaults.
type Config struct {
	// Server section — HTTP listen address and global toggles.
	Server ServerSection `yaml:"server"`

	// Auth section — keyring and bootstrap mgmt token.
	Auth AuthSection `yaml:"auth"`

	// Redis section — multi-node coherence.
	Redis RedisSection `yaml:"redis"`

	// Manager section — channel manager tunables.
	Manager ManagerSection `yaml:"manager"`

	// SinkRetry section — global retry policy + per-sink overrides.
	SinkRetry SinkRetrySection `yaml:"sink_retry"`

	// RateLimits section — per-bucket limits.
	RateLimits RateLimitsSection `yaml:"rate_limits"`

	// Observability section — metrics + tracing + access log.
	Observability ObservabilitySection `yaml:"observability"`

	// Sinks declares sink instances by user-chosen name. Each instance
	// names its kind (email | slack | webhook) plus kind-specific keys.
	// Keys not consumed by the factory are ignored at the factory level
	// but rejected at YAML decode time due to KnownFields(true).
	Sinks map[string]SinkSection `yaml:"sinks"`

	// Region labels metrics and access logs with the operator's region
	// name. Empty means single-region (no label). Surfaced in the
	// manifest as `region`.
	Region string `yaml:"region,omitempty"`

	// Peers is the informational list of peer region URLs surfaced in
	// the manifest. Parsec performs no replication against this list —
	// tooling like parsec-keys-sync reads it to discover topology.
	Peers []string `yaml:"peers,omitempty"`
}

// SinkSection declares one sink instance.
type SinkSection struct {
	Kind string `yaml:"kind"`
	// Common fields parsed by the loader; pass-through keys for the
	// factory's specifics use the catch-all Extra map.
	SMTPAddr   string            `yaml:"smtp_addr,omitempty"`
	From       string            `yaml:"from,omitempty"`
	AuthUser   string            `yaml:"auth_user,omitempty"`
	AuthPass   string            `yaml:"auth_pass,omitempty"`
	WebhookURL string            `yaml:"webhook_url,omitempty"`
	URL        string            `yaml:"url,omitempty"`
	Headers    map[string]string `yaml:"headers,omitempty"`
}

// AsFactoryRaw projects the section into the loose map[string]any the
// sinks package factories expect.
func (s SinkSection) AsFactoryRaw() map[string]any {
	raw := map[string]any{}
	if s.SMTPAddr != "" {
		raw["smtp_addr"] = s.SMTPAddr
	}
	if s.From != "" {
		raw["from"] = s.From
	}
	if s.AuthUser != "" {
		raw["auth_user"] = s.AuthUser
	}
	if s.AuthPass != "" {
		raw["auth_pass"] = s.AuthPass
	}
	if s.WebhookURL != "" {
		raw["webhook_url"] = s.WebhookURL
	}
	if s.URL != "" {
		raw["url"] = s.URL
	}
	if len(s.Headers) > 0 {
		hdr := map[string]any{}
		for k, v := range s.Headers {
			hdr[k] = v
		}
		raw["headers"] = hdr
	}
	return raw
}

// ServerSection holds the HTTP listener configuration.
type ServerSection struct {
	Addr         string              `yaml:"addr"`
	NoAuth       bool                `yaml:"no_auth"`
	WebTransport WebTransportSection `yaml:"webtransport"`
	AdminUI      AdminUISection      `yaml:"admin_ui"`
}

// AdminUISection toggles the embedded operator admin SPA. Default off.
// Operators opt in per deployment; when off the /admin/* routes are not
// mounted and return 404.
type AdminUISection struct {
	Enabled bool `yaml:"enabled"`
}

// WebTransportSection holds the opt-in HTTP/3 WebTransport listener.
// Empty addr means disabled.
type WebTransportSection struct {
	Enabled        bool     `yaml:"enabled"`
	Addr           string   `yaml:"addr"`
	CertFile       string   `yaml:"cert_file"`
	KeyFile        string   `yaml:"key_file"`
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// AuthSection holds the keyring + bootstrap mgmt token settings, plus
// the optional OIDC bridge configuration.
type AuthSection struct {
	StateDir    string `yaml:"state_dir"`
	MgmtSubject string `yaml:"mgmt_subject"`
	MgmtTTL     string `yaml:"mgmt_ttl"`     // parsed as duration
	KeyringPoll string `yaml:"keyring_poll"` // parsed as duration

	// OIDC opts the deployment into the OIDC bridge. Empty
	// (zero-value) Issuer means OIDC is disabled — the management
	// RPC accepts only HMAC tokens.
	OIDC OIDCSection `yaml:"oidc"`
}

// OIDCSection configures the OIDC bridge. Most fields map directly
// onto auth.OIDCConfig; the slice of grants is translated by
// Resolve() into []auth.OIDCGrant.
type OIDCSection struct {
	// Issuer is the IdP discovery URL (e.g.
	// https://accounts.google.com). Empty = OIDC disabled.
	Issuer string `yaml:"issuer"`

	// Audience is the configured `aud` claim — typically the client
	// identifier the operator registered with the IdP.
	Audience string `yaml:"audience"`

	// SubjectClaim names the claim that becomes the synthetic
	// Claims.Sub field. Defaults to "sub" when empty.
	SubjectClaim string `yaml:"subject_claim"`

	// ScopesClaim names the claim that carries the operator's group
	// memberships. Defaults to "groups" when empty.
	ScopesClaim string `yaml:"scopes_claim"`

	// Grants maps IdP group names onto parsec scopes + verbs.
	Grants []OIDCGrantSection `yaml:"grants"`
}

// OIDCGrantSection is one entry in the auth.oidc.grants list.
type OIDCGrantSection struct {
	IfGroup string   `yaml:"if_group"`
	Scope   string   `yaml:"scope"`
	Verbs   []string `yaml:"verbs"`
}

// RedisSection holds the cross-node Redis configuration. Empty Addr
// means "single-node in-memory mode".
type RedisSection struct {
	Addr      string `yaml:"addr"`
	KeyPrefix string `yaml:"key_prefix"`
	NodeID    string `yaml:"node_id"`
}

// ManagerSection holds channel manager tunables.
type ManagerSection struct {
	SweepInterval string `yaml:"sweep_interval"`
}

// SinkRetrySection holds the global retry policy plus optional per-sink
// overrides keyed by sink name.
type SinkRetrySection struct {
	MaxAttempts    int                   `yaml:"max_attempts"`
	BaseBackoff    string                `yaml:"base_backoff"`
	MaxBackoff     string                `yaml:"max_backoff"`
	JitterFraction float64               `yaml:"jitter_fraction"`
	PerSink        map[string]RetryEntry `yaml:"per_sink"`
}

// RetryEntry holds the overridable subset of RetryConfig.
type RetryEntry struct {
	MaxAttempts    int     `yaml:"max_attempts"`
	BaseBackoff    string  `yaml:"base_backoff"`
	MaxBackoff     string  `yaml:"max_backoff"`
	JitterFraction float64 `yaml:"jitter_fraction"`
}

// RateLimitsSection groups every rate-limit bucket.
type RateLimitsSection struct {
	Publish    BucketSection `yaml:"publish"`
	Subscribe  BucketSection `yaml:"subscribe"`
	TokenIssue BucketSection `yaml:"token_issue"`
}

// BucketSection is one configured bucket. Rate=0 disables the bucket.
type BucketSection struct {
	Rate  int    `yaml:"rate"`
	Per   string `yaml:"per"` // parsed as duration
	Burst int    `yaml:"burst"`
}

// ObservabilitySection holds metrics, tracing, and access-log tunables.
type ObservabilitySection struct {
	MetricsBearerToken string   `yaml:"metrics_bearer_token"`
	OTLPEndpoint       string   `yaml:"otlp_endpoint"`
	TrustedProxies     []string `yaml:"trusted_proxies"`
}
