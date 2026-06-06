// Package service implements the surface-agnostic business logic that the
// CLI and Twirp layers all call into. Each surface translates its own
// shape to/from these methods and never touches the broker or manager
// directly.
package service

import (
	"context"
	"runtime"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/channels"
	"github.com/frankbardon/parsec/descriptor"
	"github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/ratelimit"
)

// Service is the shared business-logic layer.
type Service struct {
	p       *parsec.Parsec
	version string
}

// New constructs a Service over a running (or about-to-run) Parsec instance.
func New(p *parsec.Parsec, version string) *Service {
	return &Service{p: p, version: version}
}

// Parsec returns the underlying instance. Used by surface code that needs
// to reach for Verifier / Issuer directly (e.g. the bearer middleware).
func (s *Service) Parsec() *parsec.Parsec { return s.p }

// Manifest returns the self-describing instance manifest.
func (s *Service) Manifest(ctx context.Context) descriptor.Envelope {
	cfg := s.p.SinkRetryConfig()
	rl := s.p.RateLimits()
	m := descriptor.Manifest{
		Service:       "parsec",
		Version:       s.version,
		GoVersion:     runtime.Version(),
		Surfaces:      []string{"cli", "twirp", "websocket"},
		Sinks:         s.p.Sinks().Names(),
		Visibilities:  []string{string(channels.VisibilityPublic), string(channels.VisibilityPrivate)},
		MaxPrivateTTL: channels.MaxPrivateTTL.String(),
		KeyRotation:   "supported",
		ActiveKeyID:   s.p.KeyRing().ActiveID(),
		Persistence:   s.p.Persistence(),
		DLQBackend:    s.p.DLQBackend(),
		SinkRetry: &descriptor.SinkRetrySummary{
			MaxAttempts: cfg.MaxAttempts,
			BaseBackoff: cfg.BaseBackoff.String(),
			MaxBackoff:  cfg.MaxBackoff.String(),
		},
		RateLimits:     &rl,
		MetricsEnabled: s.p.Metrics() != nil,
		TracingEnabled: s.p.OTLPEndpoint() != "",
		MetricsPath:    parsec.MetricsPath,
		ScopeGrammar:   "visibility:segment(.segment)*[.*][.**]",
		SupportedVerbs: supportedVerbs(),
		DenySupported:  true,
		ScopePrecedence: "deny-wins",
		ConfigSource:     s.p.ConfigSource(),
		DeltaCompression: "supported",
		Transports:       transportsList(s.p),
		Region:           s.p.Region(),
		Peers:            s.p.Peers(),
		AdminUIEnabled:   s.p.AdminUIOptions().Enabled,
		AuthOIDCEnabled:  s.p.OIDCEnabled(),
		AuthOIDCIssuer:   s.p.OIDCIssuer(),
	}
	return descriptor.NewEnvelope("parsec.manifest", m)
}

// transportsList returns the wire transports currently mounted. Always
// includes "websocket"; appends "webtransport" when the HTTP/3 listener
// is configured.
func transportsList(p *parsec.Parsec) []string {
	out := []string{"websocket"}
	if p.WebTransportOptions().Enabled() {
		out = append(out, "webtransport")
	}
	return out
}

// supportedVerbs renders auth.AllVerbs as plain strings for the manifest.
// Defined here so the descriptor package stays auth-free.
func supportedVerbs() []string {
	out := make([]string, 0, len(auth.AllVerbs))
	for _, v := range auth.AllVerbs {
		out = append(out, string(v))
	}
	return out
}

// OpenPublicChannel opens (or re-opens) a public channel.
func (s *Service) OpenPublicChannel(ctx context.Context, name string, ttl time.Duration) (*channels.Channel, error) {
	return s.p.OpenPublic(name, ttl)
}

// CreatePrivateChannel mints a private channel with access + refresh
// tokens. subjectID is recorded in the token's sub claim; pass empty
// for anonymous. scopes stamps pattern grants into both tokens; pass
// nil for an exact-match-only grant on name.
func (s *Service) CreatePrivateChannel(ctx context.Context, subjectID, name string, ttl time.Duration, scopes []auth.Scope) (parsec.Credentials, error) {
	return s.p.CreatePrivate(subjectID, name, ttl, scopes)
}

// RefreshAccess exchanges a refresh token for a new access token. The
// refresh endpoint is unauthenticated by mgmt-bearer, so the gate keys
// off the request's remote IP — operators behind a trusted proxy that
// terminates TLS should run a complementary L7 rate-limit there.
func (s *Service) RefreshAccess(ctx context.Context, refreshToken string) (parsec.RefreshResult, error) {
	if err := s.gateTokenIssue(ctx); err != nil {
		return parsec.RefreshResult{}, err
	}
	return s.p.RefreshAccessCtx(ctx, refreshToken)
}

// gateTokenIssue charges one event against the token-issue bucket keyed
// off the remote IP.
func (s *Service) gateTokenIssue(ctx context.Context) error {
	ip := parsec.RemoteIPFromContext(ctx)
	if ip == "" {
		return nil
	}
	dec, err := s.p.CheckRateLimit(ctx, ratelimit.BucketTokenIssue, ip, nil)
	if err != nil {
		return errors.Wrap(errors.Internal, "rate limiter error", err)
	}
	if !dec.Allowed {
		return errors.New(errors.RateLimited, "token refresh rate limit exceeded")
	}
	return nil
}

// ListChannels returns a snapshot of all managed channels.
func (s *Service) ListChannels(ctx context.Context) []channels.Channel {
	return s.p.Manager().List()
}

// GetChannel returns one channel by name.
func (s *Service) GetChannel(ctx context.Context, name string) (*channels.Channel, error) {
	n, err := channels.ParseName(name)
	if err != nil {
		return nil, err
	}
	return s.p.Manager().Get(n)
}

// DeleteChannel removes a channel from the manager.
func (s *Service) DeleteChannel(ctx context.Context, name string) error {
	n, err := channels.ParseName(name)
	if err != nil {
		return err
	}
	return s.p.Manager().Delete(n)
}

// Publish ships a message and returns the broker ack. The mgmt bearer's
// subject (from the request context) is the rate-limit key; when no
// subject is in context (e.g. tests without bearer enforcement) the
// remote IP is the fallback; when neither is available the call
// proceeds without gating.
func (s *Service) Publish(ctx context.Context, name string, data []byte) (PublishAck, error) {
	if err := s.gatePublish(ctx, name); err != nil {
		return PublishAck{}, err
	}
	res, err := s.p.Publish(ctx, name, data)
	if err != nil {
		return PublishAck{}, err
	}
	return PublishAck{Offset: res.Offset, Epoch: res.Epoch}, nil
}

// gatePublish charges one event against the publish bucket. The
// channel name participates: when the operator has configured a
// PerChannelPublish rule that matches, that rule's tighter (or
// looser) Limit applies and the key is namespaced per-rule so two
// rules don't share a budget. Default-bucket calls keep the original
// `publish:<subject>` key shape.
//
// Malformed channel names skip per-channel resolution and fall
// through to the default — Publish itself will reject the name
// further down the path.
func (s *Service) gatePublish(ctx context.Context, name string) error {
	key := parsec.SubjectFromContext(ctx)
	if key == "" {
		key = parsec.RemoteIPFromContext(ctx)
	}
	if key == "" {
		// No identity available — proceed without gating. The CLI sets
		// no bearer in some tests; the production HTTP surface always
		// stamps an IP at the very least.
		return nil
	}
	override := overrideFromContext(ctx)
	parsed, err := channels.ParseName(name)
	if err != nil {
		// Fall back to the per-subject bucket; the caller will hit
		// ParseName again inside Publish and surface the real error.
		dec, lerr := s.p.CheckRateLimit(ctx, ratelimit.BucketPublish, key, override)
		if lerr != nil {
			return errors.Wrap(errors.Internal, "rate limiter error", lerr)
		}
		if !dec.Allowed {
			return errors.New(errors.RateLimited, "publish rate limit exceeded")
		}
		return nil
	}
	dec, err := s.p.CheckPublishLimit(ctx, key, parsed, override)
	if err != nil {
		return errors.Wrap(errors.Internal, "rate limiter error", err)
	}
	if !dec.Allowed {
		return errors.New(errors.RateLimited, "publish rate limit exceeded")
	}
	return nil
}

// overrideFromContext returns the per-token rate-limit override, if any.
// Surface middleware planted the validated mgmt claims into ctx; the
// rl field on those claims overrides the operator-configured default.
func overrideFromContext(ctx context.Context) *ratelimit.Limit {
	c := parsec.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	return c.RateLimitOverride
}

// Presence returns the live subscriber count for a channel.
func (s *Service) Presence(ctx context.Context, name string) (int, error) {
	n, err := channels.ParseName(name)
	if err != nil {
		return 0, err
	}
	return s.p.Broker().Presence(ctx, n)
}

// PublishAck is the surface-shape of a publish result.
type PublishAck struct {
	Offset uint64 `json:"offset"`
	Epoch  string `json:"epoch"`
}

// Compile-time reference so coded errors flow through this package.
var _ = errors.ChannelInvalid
