package config

import (
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/ratelimit"
	"github.com/frankbardon/parsec/sinks"
)

// WebTransportResolved is the typed projection of the WT section.
type WebTransportResolved struct {
	Enabled        bool
	Addr           string
	CertFile       string
	KeyFile        string
	AllowedOrigins []string
}

// Resolved is the validated, type-converted projection of a Config. The
// CLI calls Resolve once after Load + flag-layer merge to get a struct
// every downstream consumer can read without re-parsing durations.
type Resolved struct {
	Source string // path the config was loaded from, "" if no file

	// Server
	Addr           string
	NoAuth         bool
	WebTransport   WebTransportResolved
	AdminUIEnabled bool

	// Auth
	StateDir    string
	MgmtSubject string
	MgmtTTL     time.Duration
	KeyringPoll time.Duration

	// OIDC carries the parsed OIDC bridge configuration when the
	// auth.oidc.issuer key is set. Nil when OIDC is disabled.
	OIDC *auth.OIDCConfig

	// Redis
	RedisAddr      string
	RedisKeyPrefix string
	NodeID         string

	// Manager
	SweepInterval time.Duration

	// Sink retry
	SinkRetry    sinks.RetryConfig
	PerSinkRetry map[string]sinks.RetryConfig

	// Rate limits
	RateLimits ratelimit.RateLimits

	// Observability
	MetricsBearerToken string
	OTLPEndpoint       string
	TrustedProxies     []net.IPNet

	// Sinks (built by the factory layer at apply time).
	Sinks []sinks.Sink

	// Region labels metrics + access logs. Empty = no label.
	Region string

	// Peers is the informational list of peer region URLs surfaced in
	// the manifest.
	Peers []string
}

// Resolve converts a validated Config into a Resolved snapshot.
// Mutually-exclusive checks have already happened during Validate.
func (c *Config) Resolve() (*Resolved, error) {
	r := &Resolved{
		Addr:   c.Server.Addr,
		NoAuth: c.Server.NoAuth,
		WebTransport: WebTransportResolved{
			Enabled:        c.Server.WebTransport.Enabled,
			Addr:           c.Server.WebTransport.Addr,
			CertFile:       c.Server.WebTransport.CertFile,
			KeyFile:        c.Server.WebTransport.KeyFile,
			AllowedOrigins: c.Server.WebTransport.AllowedOrigins,
		},
		AdminUIEnabled:     c.Server.AdminUI.Enabled,
		MgmtSubject:        c.Auth.MgmtSubject,
		RedisAddr:          c.Redis.Addr,
		RedisKeyPrefix:     c.Redis.KeyPrefix,
		NodeID:             c.Redis.NodeID,
		MetricsBearerToken: c.Observability.MetricsBearerToken,
		OTLPEndpoint:       c.Observability.OTLPEndpoint,
		Region:             c.Region,
		Peers:              append([]string(nil), c.Peers...),
	}

	if c.Auth.StateDir != "" {
		abs, err := filepath.Abs(c.Auth.StateDir)
		if err != nil {
			return nil, fmt.Errorf("config: resolve state_dir: %w", err)
		}
		r.StateDir = abs
	}
	var err error
	if r.MgmtTTL, err = mustDur("auth.mgmt_ttl", c.Auth.MgmtTTL); err != nil {
		return nil, err
	}
	if r.KeyringPoll, err = mustDur("auth.keyring_poll", c.Auth.KeyringPoll); err != nil {
		return nil, err
	}
	if r.SweepInterval, err = mustDur("manager.sweep_interval", c.Manager.SweepInterval); err != nil {
		return nil, err
	}

	r.SinkRetry, err = resolveRetry(c.SinkRetry)
	if err != nil {
		return nil, err
	}
	if len(c.SinkRetry.PerSink) > 0 {
		r.PerSinkRetry = make(map[string]sinks.RetryConfig, len(c.SinkRetry.PerSink))
		for name, entry := range c.SinkRetry.PerSink {
			cfg, err := resolveRetryEntry(name, c.SinkRetry, entry)
			if err != nil {
				return nil, err
			}
			r.PerSinkRetry[name] = cfg
		}
	}

	if r.RateLimits.Publish, err = bucketToLimit("publish", c.RateLimits.Publish); err != nil {
		return nil, err
	}
	if r.RateLimits.Subscribe, err = bucketToLimit("subscribe", c.RateLimits.Subscribe); err != nil {
		return nil, err
	}
	if r.RateLimits.TokenIssue, err = bucketToLimit("token_issue", c.RateLimits.TokenIssue); err != nil {
		return nil, err
	}

	for name, section := range c.Sinks {
		if section.Kind == "" {
			return nil, fmt.Errorf("config: sinks.%s: kind is required", name)
		}
		s, err := sinks.BuildSink(section.Kind, name, section.AsFactoryRaw())
		if err != nil {
			return nil, fmt.Errorf("config: sinks.%s: %w", name, err)
		}
		r.Sinks = append(r.Sinks, s)
	}

	if c.Auth.OIDC.Issuer != "" {
		oc, err := resolveOIDC(c.Auth.OIDC)
		if err != nil {
			return nil, err
		}
		r.OIDC = oc
	}

	for _, cidr := range c.Observability.TrustedProxies {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("config: observability.trusted_proxies %q: %w", cidr, err)
		}
		r.TrustedProxies = append(r.TrustedProxies, *ipnet)
	}

	return r, nil
}

// ApplyTo merges r into o, overwriting only fields the resolved config
// explicitly sets. Pass an already-built parsec.Options to layer in
// CLI/flag values on top by calling further setters after this returns.
func (r *Resolved) ApplyTo(o *parsec.Options) {
	if r.StateDir != "" {
		o.StateDir = r.StateDir
	}
	if r.KeyringPoll > 0 {
		o.KeyringPollInterval = r.KeyringPoll
	}
	if r.RedisAddr != "" {
		o.RedisAddr = r.RedisAddr
	}
	if r.RedisKeyPrefix != "" {
		o.RedisKeyPrefix = r.RedisKeyPrefix
	}
	if r.NodeID != "" {
		o.NodeID = r.NodeID
	}
	if r.SweepInterval > 0 {
		o.SweepInterval = r.SweepInterval
	}
	if r.SinkRetry.MaxAttempts > 0 {
		o.SinkRetry = r.SinkRetry
	}
	if r.PerSinkRetry != nil {
		o.PerSinkRetry = r.PerSinkRetry
	}
	if !r.RateLimits.Empty() {
		o.RateLimits = r.RateLimits
	}
	if r.MetricsBearerToken != "" {
		o.MetricsBearerToken = r.MetricsBearerToken
	}
	if r.OTLPEndpoint != "" {
		o.OTLPEndpoint = r.OTLPEndpoint
	}
	if len(r.TrustedProxies) > 0 {
		o.TrustedProxies = r.TrustedProxies
	}
	if len(r.Sinks) > 0 {
		if o.Sinks == nil {
			o.Sinks = sinks.NewRegistry()
		}
		for _, s := range r.Sinks {
			o.Sinks.Register(s)
		}
	}
	if r.OIDC != nil {
		o.OIDCConfig = r.OIDC
	}
	if r.WebTransport.Enabled && r.WebTransport.Addr != "" {
		o.WebTransport = parsec.WebTransportOptions{
			Addr:           r.WebTransport.Addr,
			TLSCertFile:    r.WebTransport.CertFile,
			TLSKeyFile:     r.WebTransport.KeyFile,
			AllowedOrigins: r.WebTransport.AllowedOrigins,
		}
	}
	if r.AdminUIEnabled {
		o.AdminUI = parsec.AdminUIOptions{Enabled: true}
	}
	if r.Region != "" {
		o.Region = r.Region
	}
	if len(r.Peers) > 0 {
		o.Peers = append([]string(nil), r.Peers...)
	}
}

func resolveRetry(s SinkRetrySection) (sinks.RetryConfig, error) {
	cfg := sinks.RetryConfig{
		MaxAttempts:    s.MaxAttempts,
		JitterFraction: s.JitterFraction,
	}
	var err error
	if cfg.BaseBackoff, err = mustDur("sink_retry.base_backoff", s.BaseBackoff); err != nil {
		return cfg, err
	}
	if cfg.MaxBackoff, err = mustDur("sink_retry.max_backoff", s.MaxBackoff); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func resolveRetryEntry(name string, base SinkRetrySection, e RetryEntry) (sinks.RetryConfig, error) {
	cfg := sinks.RetryConfig{
		MaxAttempts:    e.MaxAttempts,
		JitterFraction: e.JitterFraction,
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = base.MaxAttempts
	}
	if cfg.JitterFraction == 0 {
		cfg.JitterFraction = base.JitterFraction
	}
	var err error
	cfg.BaseBackoff, err = mustDur(fmt.Sprintf("sink_retry.per_sink.%s.base_backoff", name), e.BaseBackoff)
	if err != nil {
		return cfg, err
	}
	if cfg.BaseBackoff == 0 && base.BaseBackoff != "" {
		cfg.BaseBackoff, err = mustDur("sink_retry.base_backoff", base.BaseBackoff)
		if err != nil {
			return cfg, err
		}
	}
	cfg.MaxBackoff, err = mustDur(fmt.Sprintf("sink_retry.per_sink.%s.max_backoff", name), e.MaxBackoff)
	if err != nil {
		return cfg, err
	}
	if cfg.MaxBackoff == 0 && base.MaxBackoff != "" {
		cfg.MaxBackoff, err = mustDur("sink_retry.max_backoff", base.MaxBackoff)
		if err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func bucketToLimit(name string, b BucketSection) (ratelimit.Limit, error) {
	if b.Rate <= 0 {
		return ratelimit.Limit{}, nil
	}
	per, err := mustDur("rate_limits."+name+".per", b.Per)
	if err != nil {
		return ratelimit.Limit{}, err
	}
	return ratelimit.Limit{Rate: b.Rate, Per: per, Burst: b.Burst}, nil
}

func mustDur(name, raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s: parse %q: %w", name, raw, err)
	}
	return d, nil
}

// resolveOIDC translates the YAML OIDC section into an
// auth.OIDCConfig. An invalid verb in a grant or missing required key
// is a config error — fails loud at boot.
func resolveOIDC(s OIDCSection) (*auth.OIDCConfig, error) {
	if s.Audience == "" {
		return nil, fmt.Errorf("config: auth.oidc.audience is required when issuer is set")
	}
	grants := make([]auth.OIDCGrant, 0, len(s.Grants))
	for i, g := range s.Grants {
		if g.IfGroup == "" {
			return nil, fmt.Errorf("config: auth.oidc.grants[%d].if_group is required", i)
		}
		if g.Scope == "" {
			return nil, fmt.Errorf("config: auth.oidc.grants[%d].scope is required", i)
		}
		if len(g.Verbs) == 0 {
			return nil, fmt.Errorf("config: auth.oidc.grants[%d].verbs is required", i)
		}
		vs := make([]auth.Verb, 0, len(g.Verbs))
		for _, raw := range g.Verbs {
			v := auth.Verb(raw)
			if !v.Valid() {
				return nil, fmt.Errorf("config: auth.oidc.grants[%d].verbs: unknown verb %q", i, raw)
			}
			vs = append(vs, v)
		}
		grants = append(grants, auth.OIDCGrant{
			IfGroup: g.IfGroup,
			Scope:   g.Scope,
			Verbs:   vs,
		})
	}
	return &auth.OIDCConfig{
		Issuer:       s.Issuer,
		Audience:     s.Audience,
		SubjectClaim: s.SubjectClaim,
		ScopesClaim:  s.ScopesClaim,
		Grants:       grants,
	}, nil
}
