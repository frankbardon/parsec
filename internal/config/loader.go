package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// envInterp matches ${VAR_NAME} placeholders in string fields. Replaced
// with os.Getenv at load time so secrets can be sourced from the
// environment without inlining.
var envInterp = regexp.MustCompile(`\${([A-Z_][A-Z0-9_]*)}`)

// Load reads path, parses it as YAML, applies env interpolation to
// every string field, runs ApplyDefaults, and validates the result.
// KnownFields(true) on the decoder rejects unknown keys at the top
// level so typos fail at boot instead of silently being ignored.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config: empty path")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	interpolateConfig(&cfg)
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ApplyDefaults fills zero values with the same defaults the CLI uses
// today. Mutates the receiver.
func (c *Config) ApplyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8000"
	}
	if c.Auth.MgmtSubject == "" {
		c.Auth.MgmtSubject = "operator"
	}
	if c.Auth.MgmtTTL == "" {
		c.Auth.MgmtTTL = "24h"
	}
	if c.Auth.KeyringPoll == "" {
		c.Auth.KeyringPoll = "5s"
	}
	if c.Redis.KeyPrefix == "" && c.Redis.Addr != "" {
		c.Redis.KeyPrefix = "parsec"
	}
	if c.Manager.SweepInterval == "" {
		c.Manager.SweepInterval = "30s"
	}
}

// Validate checks for impossible combinations.
func (c *Config) Validate() error {
	if _, err := parseDurationField("auth.mgmt_ttl", c.Auth.MgmtTTL); err != nil {
		return err
	}
	if _, err := parseDurationField("auth.keyring_poll", c.Auth.KeyringPoll); err != nil {
		return err
	}
	if _, err := parseDurationField("manager.sweep_interval", c.Manager.SweepInterval); err != nil {
		return err
	}
	for name, p := range c.SinkRetry.PerSink {
		if p.BaseBackoff != "" {
			if _, err := parseDurationField(fmt.Sprintf("sink_retry.per_sink.%s.base_backoff", name), p.BaseBackoff); err != nil {
				return err
			}
		}
		if p.MaxBackoff != "" {
			if _, err := parseDurationField(fmt.Sprintf("sink_retry.per_sink.%s.max_backoff", name), p.MaxBackoff); err != nil {
				return err
			}
		}
	}
	if c.SinkRetry.BaseBackoff != "" {
		if _, err := parseDurationField("sink_retry.base_backoff", c.SinkRetry.BaseBackoff); err != nil {
			return err
		}
	}
	if c.SinkRetry.MaxBackoff != "" {
		if _, err := parseDurationField("sink_retry.max_backoff", c.SinkRetry.MaxBackoff); err != nil {
			return err
		}
	}
	if err := validateBucket("rate_limits.publish", c.RateLimits.Publish); err != nil {
		return err
	}
	if err := validateBucket("rate_limits.subscribe", c.RateLimits.Subscribe); err != nil {
		return err
	}
	if err := validateBucket("rate_limits.token_issue", c.RateLimits.TokenIssue); err != nil {
		return err
	}
	return nil
}

func validateBucket(name string, b BucketSection) error {
	if b.Rate <= 0 {
		return nil // unlimited / unconfigured
	}
	if b.Per == "" {
		return fmt.Errorf("config: %s.per is required when rate > 0", name)
	}
	if _, err := parseDurationField(name+".per", b.Per); err != nil {
		return err
	}
	return nil
}

func parseDurationField(name, raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s: parse %q: %w", name, raw, err)
	}
	return d, nil
}

// interpolateConfig walks the config and replaces ${ENV} placeholders
// in every string field with os.Getenv. Missing vars are replaced with
// an empty string — the validator catches the resulting mistakes.
func interpolateConfig(c *Config) {
	c.Server.Addr = interp(c.Server.Addr)
	c.Auth.StateDir = interp(c.Auth.StateDir)
	c.Auth.MgmtSubject = interp(c.Auth.MgmtSubject)
	c.Auth.MgmtTTL = interp(c.Auth.MgmtTTL)
	c.Auth.KeyringPoll = interp(c.Auth.KeyringPoll)
	c.Auth.OIDC.Issuer = interp(c.Auth.OIDC.Issuer)
	c.Auth.OIDC.Audience = interp(c.Auth.OIDC.Audience)
	c.Auth.OIDC.SubjectClaim = interp(c.Auth.OIDC.SubjectClaim)
	c.Auth.OIDC.ScopesClaim = interp(c.Auth.OIDC.ScopesClaim)
	c.Redis.Addr = interp(c.Redis.Addr)
	c.Redis.KeyPrefix = interp(c.Redis.KeyPrefix)
	c.Redis.NodeID = interp(c.Redis.NodeID)
	c.Manager.SweepInterval = interp(c.Manager.SweepInterval)
	c.SinkRetry.BaseBackoff = interp(c.SinkRetry.BaseBackoff)
	c.SinkRetry.MaxBackoff = interp(c.SinkRetry.MaxBackoff)
	for name, p := range c.SinkRetry.PerSink {
		p.BaseBackoff = interp(p.BaseBackoff)
		p.MaxBackoff = interp(p.MaxBackoff)
		c.SinkRetry.PerSink[name] = p
	}
	c.RateLimits.Publish.Per = interp(c.RateLimits.Publish.Per)
	c.RateLimits.Subscribe.Per = interp(c.RateLimits.Subscribe.Per)
	c.RateLimits.TokenIssue.Per = interp(c.RateLimits.TokenIssue.Per)
	c.Observability.MetricsBearerToken = interp(c.Observability.MetricsBearerToken)
	c.Observability.OTLPEndpoint = interp(c.Observability.OTLPEndpoint)
	for i, p := range c.Observability.TrustedProxies {
		c.Observability.TrustedProxies[i] = interp(p)
	}
}

func interp(s string) string {
	if s == "" {
		return s
	}
	return envInterp.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1] // strip ${ and }
		return os.Getenv(name)
	})
}
