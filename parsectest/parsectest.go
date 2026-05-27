// Package parsectest provides test helpers for code that embeds or
// integrates against the parsec library. The shape mirrors net/http's
// httptest: one-call constructors that return a ready instance, with
// teardown registered through testing.TB.Cleanup.
//
// Typical use:
//
//	func TestMyPublisher(t *testing.T) {
//	    p := parsectest.New(t)
//	    ch, err := p.OpenPublic("public:webapp.system.status", time.Minute)
//	    // ... exercise your code against p ...
//	}
//
// NewServer additionally mounts the parsec HTTP surface on a
// *httptest.Server so tests can hit the websocket / Twirp / manifest
// endpoints over real HTTP:
//
//	func TestMyClient(t *testing.T) {
//	    inst := parsectest.NewServer(t)
//	    bearer := inst.MintMgmt(t, "ops", time.Hour)
//	    // ... point a Twirp client at inst.BaseURL with bearer ...
//	}
//
// NewWithRedis spins up an in-process miniredis and wires the broker,
// channel registry, keyring, DLQ, and rate limiter to it — useful for
// exercising the multi-node code paths without a real Redis container.
package parsectest

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/internal/server"
	"github.com/frankbardon/parsec/ratelimit"
	"github.com/frankbardon/parsec/service"
	"github.com/frankbardon/parsec/sinks"
)

// Instance is a running *parsec.Parsec bound to test lifetime. The
// embedded pointer exposes the full library API. Server + BaseURL are
// populated only when the instance was constructed via NewServer.
type Instance struct {
	*parsec.Parsec

	// Service is the surface-agnostic business layer. RPC handlers,
	// admin endpoints, and CLI adapters all sit on top of it. Useful
	// when test code wants to call into the same logic the HTTP layer
	// would invoke.
	Service *service.Service

	// Server is the *httptest.Server hosting the parsec HTTP surface
	// (Twirp + websocket + manifest + healthz). Non-nil only when the
	// instance was built via NewServer.
	Server *httptest.Server

	// BaseURL is Server.URL (no trailing slash). Empty when Server is nil.
	BaseURL string

	cfg    *config
	cancel context.CancelFunc
}

// New constructs a *parsec.Parsec with ephemeral state and in-memory
// everything, starts Run in a goroutine, and registers cleanup via
// t.Cleanup. The returned Instance does not expose an HTTP server —
// use NewServer for that.
func New(t testing.TB, opts ...Option) *Instance {
	t.Helper()
	return build(t, false, opts...)
}

// NewServer constructs an Instance and additionally mounts the parsec
// HTTP surface on a *httptest.Server. Bearer middleware is wired
// through the parsec verifier — mint a mgmt bearer with i.MintMgmt
// before calling any management endpoint.
func NewServer(t testing.TB, opts ...Option) *Instance {
	t.Helper()
	return build(t, true, opts...)
}

func build(t testing.TB, withServer bool, opts ...Option) *Instance {
	t.Helper()
	cfg := defaultConfig(t)
	for _, o := range opts {
		o(cfg)
	}

	p, err := parsec.New(cfg.pOpts)
	if err != nil {
		t.Fatalf("parsectest: parsec.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = p.Run(ctx)
	}()

	// Wait for the broker to finish booting before returning control to
	// the test. Without this gate, the first Publish call races
	// Run/node.Run and trips PARSEC_BROKER_NOT_READY.
	deadline := time.Now().Add(5 * time.Second)
	for !p.Broker().Started() {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("parsectest: broker did not start within 5s")
		}
		time.Sleep(2 * time.Millisecond)
	}

	svc := service.New(p, cfg.version)

	inst := &Instance{Parsec: p, Service: svc, cfg: cfg, cancel: cancel}

	if withServer {
		validate := server.MgmtValidator(p)
		handler := server.New(p, svc, cfg.pOpts.Logger, validate)
		ts := httptest.NewServer(handler)
		inst.Server = ts
		inst.BaseURL = ts.URL
	}

	t.Cleanup(func() {
		if inst.Server != nil {
			inst.Server.Close()
		}
		cancel()
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Logf("parsectest: parsec.Run did not exit within 5s")
		}
	})

	return inst
}

// MintAccess returns an access token authorizing subject to subscribe
// to channel. The channel must be one that the test has already
// CreatePrivate'd (or that the subject otherwise has scope for) — this
// helper does not auto-open channels.
//
// ttl is clamped by the issuer's AccessTTL ceiling.
func (i *Instance) MintAccess(t testing.TB, subject, channel string, ttl time.Duration) string {
	t.Helper()
	pair, err := i.Issuer().IssuePair(subject, channel, ttl, nil)
	if err != nil {
		t.Fatalf("parsectest: IssuePair: %v", err)
	}
	return pair.AccessToken
}

// MintRefresh returns a refresh token for subject + channel.
func (i *Instance) MintRefresh(t testing.TB, subject, channel string, ttl time.Duration) string {
	t.Helper()
	pair, err := i.Issuer().IssuePair(subject, channel, ttl, nil)
	if err != nil {
		t.Fatalf("parsectest: IssuePair: %v", err)
	}
	return pair.RefreshToken
}

// MintMgmt returns a mgmt bearer for the named subject. ttl is clamped
// to [1h, 7d] by the issuer.
func (i *Instance) MintMgmt(t testing.TB, subject string, ttl time.Duration) string {
	t.Helper()
	if ttl == 0 {
		ttl = time.Hour
	}
	tok, _, err := i.Issuer().IssueMgmt(subject, ttl)
	if err != nil {
		t.Fatalf("parsectest: IssueMgmt: %v", err)
	}
	return tok
}

// MintPair returns the full access + refresh credential pair for a
// subject + channel. Tests that exercise the refresh-token round-trip
// (e.g. integration tests against the public RefreshToken RPC) want
// both halves.
func (i *Instance) MintPair(t testing.TB, subject, channel string, ttl time.Duration) auth.PairResult {
	t.Helper()
	pair, err := i.Issuer().IssuePair(subject, channel, ttl, nil)
	if err != nil {
		t.Fatalf("parsectest: IssuePair: %v", err)
	}
	return pair
}

// Option configures the Instance prior to construction.
type Option func(*config)

type config struct {
	pOpts   parsec.Options
	version string
}

func defaultConfig(t testing.TB) *config {
	t.Helper()
	return &config{
		pOpts: parsec.Options{
			StateDir: t.TempDir(),
			Sinks:    sinks.NewRegistry(),
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		version: "parsectest",
	}
}

// WithLogger overrides the default discard logger. Useful when a test
// wants to see parsec's boot warnings.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		c.pOpts.Logger = l
		c.pOpts.AccessLogger = l
	}
}

// WithStateDir overrides the default t.TempDir() keyring location.
// Most tests should not need this — the default is already isolated.
func WithStateDir(dir string) Option {
	return func(c *config) {
		c.pOpts.StateDir = dir
	}
}

// WithSink registers a sink with the instance's sink registry before
// parsec.New wraps the registry in retry/DLQ. The sink's Name() must
// be unique within the registry.
func WithSink(s sinks.Sink) Option {
	return func(c *config) {
		if c.pOpts.Sinks == nil {
			c.pOpts.Sinks = sinks.NewRegistry()
		}
		c.pOpts.Sinks.Register(s)
	}
}

// WithRateLimits attaches per-bucket rate limits. Pass the zero value
// of a Limit to leave a bucket unlimited.
func WithRateLimits(rl ratelimit.RateLimits) Option {
	return func(c *config) {
		c.pOpts.RateLimits = rl
	}
}

// WithRedis attaches a pre-built go-redis client. The broker,
// registry, keyring, DLQ, and rate limiter all switch to the Redis
// backends. Use NewWithRedis for an automatic miniredis-backed setup.
func WithRedis(client redis.UniversalClient) Option {
	return func(c *config) {
		c.pOpts.RedisClient = client
	}
}

// WithOptions exposes the underlying parsec.Options for the corner
// cases where a test needs a setting parsectest does not surface
// directly. Returned closure receives a pointer to the options before
// parsec.New runs; mutate freely.
func WithOptions(fn func(*parsec.Options)) Option {
	return func(c *config) {
		fn(&c.pOpts)
	}
}
