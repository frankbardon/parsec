// Package parsec is a scalable realtime messaging engine built on the
// github.com/centrifugal/centrifuge OSS Go library.
//
// Parsec ships as a CLI binary and as an embeddable Go library. The library
// is the primary deliverable; the CLI in cmd/parsec is a thin adapter over
// it, and the Twirp surface is a sibling to the CLI.
//
// Concepts:
//
//   - Channel — a named pub/sub topic. Names follow the public/private
//     namespace grammar in package channels.
//   - Broker — the centrifuge Node wrapper that holds connections and ships
//     publications.
//   - Manager — channel lifecycle (open, create-private, touch, sweep,
//     delete). TTL enforcement.
//   - Sink — out-of-band delivery (email, slack, webhook) when a session is
//     no longer listening.
//   - KeyRing — N HMAC keys with one active signer; rotated without
//     downtime.
//   - Issuer — mints access / refresh / mgmt tokens using the active key.
package parsec

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/afero"
	"go.opentelemetry.io/otel/trace"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/broker"
	"github.com/frankbardon/parsec/channels"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/internal/metrics"
	"github.com/frankbardon/parsec/internal/tracing"
	"github.com/frankbardon/parsec/ratelimit"
	"github.com/frankbardon/parsec/sinks"
	"github.com/frankbardon/parsec/tokenbroker"
)

// Options configures a Parsec instance. All fields are optional.
type Options struct {
	// FS is the filesystem used for any persistence. Defaults to OsFs.
	FS afero.Fs

	// Sinks is the registry of out-of-band delivery sinks.
	Sinks *sinks.Registry

	// KeyRing holds the HMAC signing keys. If nil, parsec.New chooses
	// between StateDir-backed bootstrap and an ephemeral in-memory ring
	// (logged loudly so the operator knows tokens won't survive restart).
	KeyRing *auth.KeyRing

	// StateDir, when set, makes parsec.New load (or bootstrap) the
	// keyring from <StateDir>/keyring.json. Ignored if KeyRing is
	// non-nil. The directory is created with 0700; the file with 0600.
	StateDir string

	// KeyringPollInterval enables the mtime-poll watcher when > 0 and a
	// StateDir is set. Default 5s.
	KeyringPollInterval time.Duration

	// RedisClient enables multi-node mode. When set, channel registry,
	// keyring, DLQ, and rate limiter all share this client by default.
	// The zero value (nil) means single-node in-memory.
	RedisClient redis.UniversalClient

	// RedisAddr is a convenience: when set and RedisClient is nil,
	// parsec.New constructs a default go-redis client. Address syntax
	// accepts the same forms as centrifuge.RedisShardConfig.Address
	// (host:port, redis://..., redis+sentinel://..., etc.).
	RedisAddr string

	// RedisShards configures the centrifuge Redis broker. When set, the
	// broker switches from in-memory to Redis-backed. If empty and
	// RedisAddr is set, parsec.New builds a single shard from RedisAddr.
	RedisShards []*centrifuge.RedisShard

	// RedisKeyPrefix namespaces every Parsec key in Redis. Default
	// "parsec". Override when multiple Parsec deployments share an
	// instance.
	RedisKeyPrefix string

	// NodeID identifies this Parsec process across the cluster. Used to
	// dedupe cross-node pub/sub events. Empty means auto-generate.
	NodeID string

	// AccessTokenTTL overrides the default access-token lifetime (5m).
	AccessTokenTTL time.Duration

	// MaxRefreshTokenTTL overrides the default refresh-token cap (1h).
	MaxRefreshTokenTTL time.Duration

	// RefreshStore tracks redeemed refresh JTIs and revoked rotation
	// families. When nil and a RedisClient is configured parsec.New
	// builds a RedisRefreshStore; otherwise a MemoryRefreshStore. The
	// store gates RefreshAccess so a leaked refresh token cannot be
	// redeemed twice without revoking the entire family.
	RefreshStore auth.RefreshStore

	// MemoryRefreshPruneInterval is the cleanup interval used when
	// parsec.New constructs the default MemoryRefreshStore. Default
	// 5 minutes. Ignored when RefreshStore is explicit.
	MemoryRefreshPruneInterval time.Duration

	// BrokerOptions are forwarded to broker.New. The SubscribeAuthorizer
	// is overridden by parsec.New to use the Verifier.
	BrokerOptions broker.Options

	// SweepInterval is how often the channel manager runs Sweep. Default 30s.
	SweepInterval time.Duration

	// SinkRetry configures the sink-side retry loop used by PublishOrSink.
	// Defaults are filled by sinks.RetryConfig.Normalize: 5 attempts,
	// 1s..30s exponential backoff, 0.2 jitter. Set MaxAttempts=1 to
	// disable retries.
	SinkRetry sinks.RetryConfig

	// PerSinkRetry holds per-sink-name overrides of SinkRetry. Useful
	// when email needs longer backoffs than slack, for instance.
	PerSinkRetry map[string]sinks.RetryConfig

	// DLQ overrides the default DLQ. When nil, parsec.New constructs a
	// sinks.RedisDLQ if RedisClient is set, otherwise a MemoryDLQ.
	DLQ sinks.DLQ

	// Logger receives boot warnings and reload events. Optional.
	Logger *slog.Logger

	// MetricsRegistry, when non-nil, is used as the Prometheus registry
	// the /metrics endpoint scrapes. When nil, parsec.New constructs a
	// dedicated registry (Go + process collectors included) — set this
	// when the embedder already runs prometheus.DefaultRegisterer and
	// wants Parsec metrics to share that universe.
	MetricsRegistry *prometheus.Registry

	// MetricsBearerToken gates the /metrics endpoint. When set, requests
	// must present Authorization: Bearer <token> matching this value
	// exactly. When empty, /metrics is unguarded — appropriate only when
	// the route is blocked at the network layer.
	MetricsBearerToken string

	// OTLPEndpoint enables OpenTelemetry tracing when non-empty. The
	// value is passed to otlptracehttp.WithEndpoint (host:port form, no
	// scheme). Empty means "tracing disabled" and the hot path performs
	// zero allocations.
	OTLPEndpoint string

	// AccessLogger receives the per-request INFO line from the HTTP
	// access-log middleware. Defaults to Logger when nil.
	AccessLogger *slog.Logger

	// TrustedProxies is the CIDR list of upstream proxies whose
	// X-Forwarded-For headers Parsec will honor. Anything else is
	// ignored to prevent client-IP spoofing.
	TrustedProxies []net.IPNet

	// WebTransport, when populated with a UDP listen address + TLS cert
	// pair, enables the HTTP/3 WebTransport handler. Default: disabled.
	WebTransport WebTransportOptions

	// AdminUI controls the embedded operator SPA. Default: disabled —
	// the bytes are kept out of the response graph entirely (every
	// /admin/* path returns 404 when off).
	AdminUI AdminUIOptions

	// ConfigSource is the path the TOML config was loaded from. The CLI
	// stamps this from `parsec serve --config <path>`; the library
	// facade carries it through to the manifest for operator visibility.
	// Embedders typically leave it empty.
	ConfigSource string

	// Limiter, when non-nil, is the rate-limit backend used by every
	// gated surface (publish / subscribe / token-issue). When nil and
	// RateLimits has any non-zero bucket, parsec.New builds one:
	// RedisLimiter when RedisClient is set, MemoryLimiter otherwise.
	Limiter ratelimit.Limiter

	// RateLimits configures the default per-subject budgets for each
	// gated bucket. The zero value (every Limit.Rate == 0) means
	// "unlimited" — no gating, no Redis traffic.
	RateLimits ratelimit.RateLimits

	// PerChannelPublishLimits, when non-empty, overrides RateLimits.Publish
	// for channels whose name matches a configured pattern. Keys are
	// channel-name patterns (see channels.ParsePattern grammar); values
	// are the Limit applied to publishes to channels matching that
	// pattern. parsec.New compiles each pattern at boot — an invalid
	// pattern aborts startup so misconfigurations fail loud. Per-rule
	// keys are namespaced separately from the default bucket, so a
	// hot channel cannot drain the global publish budget.
	PerChannelPublishLimits map[string]ratelimit.Limit

	// OIDCConfig, when non-nil, enables the OIDC bridge: management
	// RPC requests carrying an IdP-issued ID token bearer are
	// translated into a synthetic mgmt Claims and accepted alongside
	// HMAC-signed parsec mgmt tokens. nil = OIDC disabled (default).
	//
	// See auth.OIDCConfig for the full shape. parsec.New discovers
	// the issuer at boot — discovery failures abort startup so
	// misconfigured deployments fail loud.
	OIDCConfig *auth.OIDCConfig

	// Region labels metrics and access logs with the operator's region
	// name (e.g. "us-east"). When empty no label is attached, so
	// single-region deployments stay label-free. The value is also
	// surfaced in the manifest so peers can discover topology.
	Region string

	// Peers is the informational list of peer region URLs. Surfaced in
	// the manifest so operators (and tooling like parsec-keys-sync) can
	// discover the multi-region topology. Parsec itself does no peer
	// validation or active replication against this list — it is purely
	// declarative.
	Peers []string

	// SchemaHandler, when non-nil, is mounted at /parsec/schemas. The
	// HTTP surface delegates to the supplied handler so the embedder
	// can plug a schema.MemoryRegistry (or any other Registry impl)
	// without parsec.go importing the schema package itself.
	SchemaHandler http.Handler

	// JWKSHandler, when non-nil, is mounted at /parsec/jwks.json. The
	// handler is expected to serve the active KeyRing's asymmetric
	// public keys as an RFC 7517 JWKS document. Embedders that only
	// use HMAC signing should leave this nil — there is nothing to
	// publish, and the 404 prevents probing.
	JWKSHandler http.Handler

	// TokenBrokerHandler, when non-nil, is mounted under /parsec
	// (/parsec/token, /parsec/token/delegate, /parsec/revoke). The
	// embedder constructs a tokenbroker.Broker, configures its
	// Authenticator / Authorizer, and passes Broker.Handler() here.
	TokenBrokerHandler http.Handler

	// RevocationStore, when non-nil, is consulted on every private
	// channel subscribe attempt. The subscribe authorizer checks the
	// access token's JTI and the user's blanket revoke timestamp; a
	// match denies the subscribe with PARSEC_AUTH_DENIED. Pair this
	// with a tokenbroker.Broker so revoke RPCs from operators land
	// somewhere both the broker and the subscribe path can read.
	RevocationStore tokenbroker.RevocationStore

	// TelemetryHandler, when non-nil, is mounted at /parsec/metrics
	// and serves the aggregated JSON snapshot. The native /metrics
	// Prometheus endpoint is unaffected.
	TelemetryHandler http.Handler
}

// OIDCEnabled reports whether the OIDC bridge is configured. Convenience
// shorthand for the manifest layer.
func (o Options) OIDCEnabled() bool { return o.OIDCConfig != nil }

// Parsec is the library facade. Construct with New, drive with Run, publish
// with Publish.
type Parsec struct {
	opts         Options
	broker       *broker.Broker
	manager      *channels.Manager
	ring         *auth.KeyRing
	signer       *auth.Signer
	verifier     *auth.Verifier
	composite    *auth.CompositeVerifier // wraps verifier; nil iff OIDC is off (verifier still set)
	oidcVerifier *auth.OIDCVerifier      // nil = OIDC disabled
	issuer       *auth.Issuer
	logger       *slog.Logger
	keyringStore auth.KeyRingStore // nil for ephemeral / explicit ring

	keyringPath string // empty when not file-backed

	metrics        *metrics.Metrics
	tracer         trace.Tracer
	tracerShutdown tracing.ShutdownFunc

	dlq         sinks.DLQ
	dlqBackend  string // "memory" | "redis"
	wrappedReg  *sinks.Registry

	limiter    ratelimit.Limiter
	rateLimits ratelimit.RateLimits

	refreshStore auth.RefreshStore
}

// New constructs a Parsec instance. The broker is created but not started;
// call Run on the returned instance to boot the centrifuge Node and the
// sweeper goroutine.
func New(opts Options) (*Parsec, error) {
	if opts.FS == nil {
		opts.FS = afero.NewOsFs()
	}
	if opts.Sinks == nil {
		opts.Sinks = sinks.NewRegistry()
	}
	if opts.SweepInterval == 0 {
		opts.SweepInterval = 30 * time.Second
	}
	if opts.KeyringPollInterval == 0 {
		opts.KeyringPollInterval = 5 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	ring, keyringStore, keyringPath, err := buildKeyRing(opts, logger)
	if err != nil {
		return nil, err
	}
	signer, err := auth.NewSigner(ring)
	if err != nil {
		return nil, perr.Wrap(perr.AuthDenied, "build signer", err)
	}
	verifier, err := auth.NewVerifier(ring)
	if err != nil {
		return nil, perr.Wrap(perr.AuthDenied, "build verifier", err)
	}
	// OnVerify hook is installed below once metrics are constructed.

	// OIDC bridge: when an OIDCConfig is provided, discover the
	// issuer + build a verifier. Discovery failure aborts boot — a
	// half-configured OIDC deployment must not silently degrade to
	// HMAC-only.
	var oidcVerifier *auth.OIDCVerifier
	var compositeVerifier *auth.CompositeVerifier
	if opts.OIDCConfig != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ov, oerr := auth.NewOIDCVerifier(ctx, *opts.OIDCConfig)
		cancel()
		if oerr != nil {
			return nil, perr.Wrap(perr.AuthDenied, "build oidc verifier", oerr)
		}
		oidcVerifier = ov
		cv, cerr := auth.NewCompositeVerifier(verifier, oidcVerifier)
		if cerr != nil {
			return nil, perr.Wrap(perr.Internal, "build composite verifier", cerr)
		}
		compositeVerifier = cv
		logger.Info("parsec: OIDC bridge enabled", "issuer", opts.OIDCConfig.Issuer, "audience", opts.OIDCConfig.Audience)
	}

	issuer := auth.NewIssuer(signer)
	if opts.AccessTokenTTL > 0 {
		issuer.AccessTTL = opts.AccessTokenTTL
	}
	if opts.MaxRefreshTokenTTL > 0 {
		issuer.MaxRefreshTTL = opts.MaxRefreshTokenTTL
	}

	if err := normalizeRedisOptions(&opts); err != nil {
		return nil, err
	}
	// Compile per-channel rules before resolving the limiter so the
	// rules end up on the RateLimits we hand back to callers.
	rules, err := ratelimit.CompileChannelRules(opts.PerChannelPublishLimits)
	if err != nil {
		return nil, perr.Wrap(perr.InvalidArgument, "per-channel publish limit", err)
	}
	opts.RateLimits.PerChannelPublish = rules
	// Resolve the rate limiter: explicit > built-from-RateLimits.
	limiter, rl := resolveLimiter(opts)
	manager := buildManager(opts, logger)
	if opts.BrokerOptions.SubscribeAuthorizer == nil {
		tokenAuth := auth.NewSubscribeAuthorizerWithLimiter(verifier, limiter, rl.Subscribe)
		revStore := opts.RevocationStore
		opts.BrokerOptions.SubscribeAuthorizer = func(ctx context.Context, userID string, ch channels.Name, event centrifuge.SubscribeEvent) error {
			if !manager.IsOpen(ch) {
				return perr.New(perr.ChannelClosed, "channel not open")
			}
			if err := tokenAuth(ctx, userID, ch, event); err != nil {
				return err
			}
			if revStore == nil || !ch.IsPrivate() || event.Token == "" {
				return nil
			}
			claims, err := verifier.Verify(event.Token, auth.TypeAccess)
			if err != nil {
				// tokenAuth already validated; a re-verify failure here
				// is a transient race we don't want to silently allow.
				return auth.MapErr(err)
			}
			if claims.JTI != "" {
				revoked, err := revStore.IsRevoked(ctx, claims.JTI)
				if err != nil {
					return perr.Wrap(perr.Internal, "revocation check error", err)
				}
				if revoked {
					return perr.New(perr.AuthDenied, "token has been revoked")
				}
			}
			if claims.Sub != "" {
				issuedAt := time.Unix(claims.Iat, 0).UTC()
				urev, err := revStore.IsUserRevoked(ctx, claims.Sub, issuedAt)
				if err != nil {
					return perr.Wrap(perr.Internal, "revocation check error", err)
				}
				if urev {
					return perr.New(perr.AuthDenied, "user tokens have been revoked")
				}
			}
			return nil
		}
	}
	m := metrics.NewWithRegistryAndRegion(opts.MetricsRegistry, opts.Region)
	verifier.OnVerify = func(parsed, expected auth.Type, verr error) {
		// Use expected when the parse failed before payload decode (typ
		// is empty) so we never tag with the empty label value.
		t := string(parsed)
		if t == "" {
			t = string(expected)
		}
		if t == "" {
			t = "unknown"
		}
		result := metrics.ResultSuccess
		if verr != nil {
			result = metrics.ResultFailure
		}
		m.TokenVerificationTotal.WithLabelValues(t, result).Inc()
	}
	if opts.BrokerOptions.OnSubscriberChange == nil {
		opts.BrokerOptions.OnSubscriberChange = func(ch channels.Name, delta int) {
			vis := metrics.VisibilityLabel(string(ch.Visibility))
			if delta > 0 {
				m.SubscribersActive.WithLabelValues(vis).Add(float64(delta))
			} else if delta < 0 {
				m.SubscribersActive.WithLabelValues(vis).Add(float64(delta))
			}
		}
	}
	if opts.BrokerOptions.DeltaProvider == nil {
		opts.BrokerOptions.DeltaProvider = func(ch channels.Name) bool {
			return manager.IsDelta(ch)
		}
	}
	manager.SetOnSweep(func(list []channels.Channel) {
		// Reset every cell — Sweep is the periodic source of truth.
		m.ChannelsActive.Reset()
		for _, ch := range list {
			vis := metrics.VisibilityLabel(string(ch.Name.Visibility))
			m.ChannelsActive.WithLabelValues(string(ch.State), vis).Inc()
		}
	})
	br, err := broker.New(opts.BrokerOptions)
	if err != nil {
		return nil, err
	}
	tracer, shutdown, err := tracing.NewTracer(opts.OTLPEndpoint)
	if err != nil {
		return nil, perr.Wrap(perr.Internal, "tracing init", err)
	}
	if opts.AccessLogger == nil {
		opts.AccessLogger = logger
	}

	// Resolve refresh store: explicit > Redis > in-memory.
	refreshStore := buildRefreshStore(opts)

	// Resolve DLQ: explicit > Redis-backed > in-memory.
	dlq, backend := buildDLQ(opts)

	// Wrap each registered sink with the metrics observer first, then in
	// the Retrier. Order matters: the metrics envelope records every
	// attempt (including each retry as its own observation), while the
	// retry envelope handles backoff/DLQ semantics on top.
	observed := observeSinks(opts.Sinks, m)
	wrapped := wrapSinks(observed, opts.SinkRetry, opts.PerSinkRetry, dlq)

	// Hook the DLQ's registry-lookup back at the (wrapped) registry so
	// Replay can find the original sink.
	if rl, ok := dlq.(interface{ SetRegistryLookup(sinks.RegistryLookup) }); ok {
		rl.SetRegistryLookup(wrapped.Get)
	}

	p := &Parsec{
		opts:           opts,
		broker:         br,
		manager:        manager,
		ring:           ring,
		signer:         signer,
		verifier:       verifier,
		composite:      compositeVerifier,
		oidcVerifier:   oidcVerifier,
		issuer:         issuer,
		logger:         logger,
		keyringStore:   keyringStore,
		keyringPath:    keyringPath,
		metrics:        m,
		tracer:         tracer,
		tracerShutdown: shutdown,
		dlq:            dlq,
		dlqBackend:     backend,
		wrappedReg:     wrapped,
		limiter:        limiter,
		rateLimits:     rl,
		refreshStore:   refreshStore,
	}
	// Replace the user-supplied registry pointer with the wrapped one so
	// PublishOrSink and Sinks() see the retry-aware shape.
	p.opts.Sinks = wrapped
	// Auto-wire JWKS when the ring carries any asymmetric key and the
	// embedder did not supply a handler. Symmetric-only deployments
	// see no JWKS surface at all.
	if p.opts.JWKSHandler == nil && ringHasAsymmetric(p.ring) {
		p.opts.JWKSHandler = auth.JWKSHandler(p.ring)
	}
	return p, nil
}

// ringHasAsymmetric reports whether ring carries any non-retired
// asymmetric key. Used to decide whether to expose /parsec/jwks.json.
func ringHasAsymmetric(ring *auth.KeyRing) bool {
	if ring == nil {
		return false
	}
	for _, k := range ring.List() {
		if k.Role == auth.RoleRetired {
			continue
		}
		if k.Alg.IsAsymmetric() {
			return true
		}
	}
	return false
}

// buildRefreshStore resolves the refresh-rotation store. Explicit
// option wins; otherwise Redis when a client is configured, in-memory
// fallback otherwise. The default memory pruner runs every 5 minutes
// so expired records do not accumulate indefinitely.
func buildRefreshStore(opts Options) auth.RefreshStore {
	if opts.RefreshStore != nil {
		return opts.RefreshStore
	}
	if opts.RedisClient != nil {
		prefix := opts.RedisKeyPrefix
		if prefix == "" {
			prefix = "parsec"
		}
		return auth.NewRedisRefreshStore(opts.RedisClient).WithKeyPrefix(prefix)
	}
	interval := opts.MemoryRefreshPruneInterval
	if interval == 0 {
		interval = 5 * time.Minute
	}
	return auth.NewMemoryRefreshStore(interval)
}

// buildDLQ returns the DLQ to thread through retry-wrapping. The default
// rule: RedisDLQ when a redis client is configured; MemoryDLQ otherwise.
// An explicit Options.DLQ overrides both.
func buildDLQ(opts Options) (sinks.DLQ, string) {
	if opts.DLQ != nil {
		switch opts.DLQ.(type) {
		case *sinks.RedisDLQ:
			return opts.DLQ, "redis"
		case *sinks.MemoryDLQ:
			return opts.DLQ, "memory"
		default:
			return opts.DLQ, "custom"
		}
	}
	if opts.RedisClient != nil {
		prefix := opts.RedisKeyPrefix
		if prefix == "" {
			prefix = "parsec"
		}
		return sinks.NewRedisDLQ(opts.RedisClient).WithKeyPrefix(prefix), "redis"
	}
	return sinks.NewMemoryDLQ(), "memory"
}

// observeSinks returns a new Registry whose values are metrics-wrapped
// versions of the input registry's sinks. The wrapper records duration +
// attempt counters at the Send boundary.
func observeSinks(in *sinks.Registry, m *metrics.Metrics) *sinks.Registry {
	if m == nil {
		return in
	}
	out := sinks.NewRegistry()
	for _, name := range in.Names() {
		s := in.Get(name)
		if s == nil {
			continue
		}
		out.Register(metrics.WrapSink(s, m))
	}
	return out
}

// wrapSinks returns a new Registry whose values are Retrier-wrapped versions
// of the input registry's sinks. Per-sink overrides in perSink win over the
// default cfg.
func wrapSinks(in *sinks.Registry, cfg sinks.RetryConfig, perSink map[string]sinks.RetryConfig, dlq sinks.DLQ) *sinks.Registry {
	out := sinks.NewRegistry()
	for _, name := range in.Names() {
		s := in.Get(name)
		if s == nil {
			continue
		}
		c := cfg
		if override, ok := perSink[name]; ok {
			c = override
		}
		out.Register(sinks.WrapSink(s, c, dlq))
	}
	return out
}

// normalizeRedisOptions fills in derived defaults so other plumbing can
// assume coherent state. If RedisAddr is set without RedisClient, it
// builds the client; if RedisShards is empty and Redis is configured,
// it builds a single centrifuge.RedisShard from the same address.
// Also threads RedisShards into BrokerOptions.
func normalizeRedisOptions(opts *Options) error {
	if opts.RedisClient == nil && opts.RedisAddr != "" {
		opts.RedisClient = redis.NewClient(&redis.Options{Addr: opts.RedisAddr})
	}
	if opts.RedisClient != nil && len(opts.RedisShards) == 0 && opts.RedisAddr != "" {
		node, err := centrifuge.New(centrifuge.Config{})
		if err != nil {
			return perr.Wrap(perr.Internal, "centrifuge.New for shard probe", err)
		}
		shard, err := centrifuge.NewRedisShard(node, centrifuge.RedisShardConfig{Address: opts.RedisAddr})
		if err != nil {
			return perr.Wrap(perr.Internal, "build centrifuge redis shard", err)
		}
		opts.RedisShards = []*centrifuge.RedisShard{shard}
	}
	if len(opts.RedisShards) > 0 {
		opts.BrokerOptions.RedisShards = opts.RedisShards
		if opts.RedisKeyPrefix != "" && opts.BrokerOptions.RedisBrokerPrefix == "" {
			opts.BrokerOptions.RedisBrokerPrefix = opts.RedisKeyPrefix
		}
	}
	return nil
}

// resolveLimiter picks the rate-limit backend from Options. An explicit
// Options.Limiter wins. Otherwise, if RateLimits has any non-zero bucket
// we construct one: RedisLimiter when RedisClient is set, MemoryLimiter
// otherwise. Returns (nil, zero) when no limiting is configured.
//
// The Limit threaded into the limiter at construction is the max-burst of
// any bucket so a single instance handles publish/subscribe/token-issue
// keys; the per-bucket ceilings are enforced at the call site by passing
// the right key prefix (publish:.., subscribe:.., token-issue:..). The
// limiter does not need to know which bucket it is gating — the key
// already carries that context.
func resolveLimiter(opts Options) (ratelimit.Limiter, ratelimit.RateLimits) {
	rl := opts.RateLimits.Normalize()
	if opts.Limiter != nil {
		return opts.Limiter, rl
	}
	if rl.Empty() {
		return nil, rl
	}
	widest := widestLimit(rl)
	if opts.RedisClient != nil {
		prefix := opts.RedisKeyPrefix
		if prefix == "" {
			prefix = "parsec"
		}
		return ratelimit.NewRedisLimiter(opts.RedisClient, widest).WithKeyPrefix(prefix), rl
	}
	return ratelimit.NewMemoryLimiter(widest), rl
}

// widestLimit returns the per-bucket limit with the highest burst ceiling.
// A single MemoryLimiter / RedisLimiter is shared across buckets and keys
// are namespaced with the bucket prefix; the limiter must therefore allow
// the largest configured burst — the call-site keys then naturally
// partition the budget.
//
// Operators who need per-bucket distinct shapes should instantiate a
// custom Limiter and supply it via Options.Limiter.
func widestLimit(rl ratelimit.RateLimits) ratelimit.Limit {
	best := ratelimit.Limit{}
	candidates := []ratelimit.Limit{rl.Publish, rl.Subscribe, rl.TokenIssue}
	for _, r := range rl.PerChannelPublish {
		candidates = append(candidates, r.Limit)
	}
	for _, l := range candidates {
		n := l.Normalize()
		if n.Rate == 0 {
			continue
		}
		if n.Burst > best.Burst || best.Rate == 0 {
			best = n
		}
	}
	return best
}

// Limiter returns the resolved rate-limit backend, or nil when no
// limiting is configured. Surface code calls this at request time.
func (p *Parsec) Limiter() ratelimit.Limiter { return p.limiter }

// RateLimits returns the normalized default budgets. Exposed to manifest
// + service code so /manifest can advertise the configured ceilings.
func (p *Parsec) RateLimits() ratelimit.RateLimits { return p.rateLimits }

// CheckRateLimit charges one event against bucket:subject and reports
// the decision. When no limiter is configured the call returns
// AllowDecisionUnlimited without touching state. When override is
// non-nil and tighter than the configured default, the override
// applies — surfacing per-token rate-limit claims.
func (p *Parsec) CheckRateLimit(ctx context.Context, bucket, subject string, override *ratelimit.Limit) (ratelimit.Decision, error) {
	if p.limiter == nil {
		return ratelimit.AllowDecisionUnlimited, nil
	}
	effective := p.rateLimitForBucket(bucket)
	if override != nil && !override.Unlimited() {
		effective = override.Normalize()
	}
	if effective.Unlimited() {
		return ratelimit.AllowDecisionUnlimited, nil
	}
	key := bucket + ":" + subject
	// Both limiter implementations expose AllowWithLimit so the per-call
	// effective ceiling overrides whatever Limit the limiter was
	// constructed with. The Limiter interface itself stays narrow.
	type withLimit interface {
		AllowWithLimit(ctx context.Context, key string, n int, lim ratelimit.Limit) (ratelimit.Decision, error)
	}
	var dec ratelimit.Decision
	var err error
	if w, ok := p.limiter.(withLimit); ok {
		dec, err = w.AllowWithLimit(ctx, key, 1, effective)
	} else {
		dec, err = p.limiter.Allow(ctx, key, 1)
	}
	if err != nil {
		return ratelimit.Decision{}, err
	}
	result := "allowed"
	if !dec.Allowed {
		result = "denied"
	}
	if p.metrics != nil && p.metrics.RateLimitDecisions != nil {
		p.metrics.RateLimitDecisions.WithLabelValues(bucket, result).Inc()
	}
	return dec, nil
}

// CheckPublishLimit is CheckRateLimit specialised for the publish
// bucket with per-channel rule resolution: the channel's name is
// matched against the operator's PerChannelPublish rules and the
// most-specific match wins. When no rule matches, the global publish
// default applies. Per-rule and per-token-override are honored in
// that order — override beats the rule.
//
// The bucket key namespaces channels: rule-matched calls use
// `publish-channel:<rule-raw>:<subject>`, default calls use
// `publish:<subject>` (unchanged). That keeps a hot channel from
// eating the global budget.
func (p *Parsec) CheckPublishLimit(ctx context.Context, subject string, channel channels.Name, override *ratelimit.Limit) (ratelimit.Decision, error) {
	if p.limiter == nil {
		return ratelimit.AllowDecisionUnlimited, nil
	}
	effective, ruleRaw := p.rateLimits.MatchPublish(channel)
	if override != nil && !override.Unlimited() {
		effective = override.Normalize()
	}
	if effective.Unlimited() {
		return ratelimit.AllowDecisionUnlimited, nil
	}
	bucket := ratelimit.BucketPublish
	keyPrefix := bucket
	if ruleRaw != "" {
		// Distinct prefix per rule so two rules' subjects don't share
		// a key. The rule raw string is part of operator config, so
		// it's bounded cardinality.
		keyPrefix = bucket + "-channel:" + ruleRaw
	}
	key := keyPrefix + ":" + subject
	type withLimit interface {
		AllowWithLimit(ctx context.Context, key string, n int, lim ratelimit.Limit) (ratelimit.Decision, error)
	}
	var dec ratelimit.Decision
	var err error
	if w, ok := p.limiter.(withLimit); ok {
		dec, err = w.AllowWithLimit(ctx, key, 1, effective)
	} else {
		dec, err = p.limiter.Allow(ctx, key, 1)
	}
	if err != nil {
		return ratelimit.Decision{}, err
	}
	result := "allowed"
	if !dec.Allowed {
		result = "denied"
	}
	if p.metrics != nil && p.metrics.RateLimitDecisions != nil {
		p.metrics.RateLimitDecisions.WithLabelValues(bucket, result).Inc()
	}
	return dec, nil
}

// rateLimitForBucket returns the configured default for a bucket label.
// Unknown buckets default to unlimited so callers passing a freshly
// added bucket name don't accidentally inherit an unrelated default.
func (p *Parsec) rateLimitForBucket(bucket string) ratelimit.Limit {
	switch bucket {
	case ratelimit.BucketPublish:
		return p.rateLimits.Publish
	case ratelimit.BucketSubscribe:
		return p.rateLimits.Subscribe
	case ratelimit.BucketTokenIssue:
		return p.rateLimits.TokenIssue
	default:
		return ratelimit.Limit{}
	}
}

// buildManager constructs the channel manager with either a Redis-backed
// or in-memory store, wiring the cross-node event bus when Redis is
// configured.
func buildManager(opts Options, logger *slog.Logger) *channels.Manager {
	if opts.RedisClient == nil {
		return channels.NewManager()
	}
	prefix := opts.RedisKeyPrefix
	if prefix == "" {
		prefix = "parsec"
	}
	store := channels.NewRedisStore(opts.RedisClient).WithKeyPrefix(prefix)
	m := channels.NewManagerWithStore(store)
	bus := channels.NewRedisEventBus(opts.RedisClient, opts.NodeID).WithKeyPrefix(prefix)
	m.SetEventBus(bus)
	logger.Info("parsec: redis-backed channel registry active",
		"key_prefix", prefix, "node_id", bus.NodeID())
	return m
}

// buildKeyRing resolves the KeyRing precedence: explicit > Redis >
// StateDir > ephemeral. Returns the chosen ring, the keyring store
// (for reload), and the on-disk path (empty when no file persistence).
func buildKeyRing(opts Options, logger *slog.Logger) (*auth.KeyRing, auth.KeyRingStore, string, error) {
	if opts.KeyRing != nil {
		return opts.KeyRing, nil, "", nil
	}
	if opts.RedisClient != nil {
		prefix := opts.RedisKeyPrefix
		if prefix == "" {
			prefix = "parsec"
		}
		store := auth.NewRedisKeyRingStore(opts.RedisClient).WithKeyPrefix(prefix)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ring, bootstrapped, err := store.Ensure(ctx)
		if err != nil {
			return nil, nil, "", perr.Wrap(perr.Internal, "load redis keyring", err)
		}
		if bootstrapped {
			logger.Warn("parsec: bootstrapped a new keyring in redis",
				"prefix", prefix, "active_key_id", ring.ActiveID())
		} else {
			logger.Info("parsec: loaded keyring from redis",
				"prefix", prefix, "active_key_id", ring.ActiveID())
		}
		return ring, store, "", nil
	}
	if opts.StateDir != "" {
		path := filepath.Join(opts.StateDir, auth.KeyringFileName)
		store := auth.NewFileKeyRingStore(path, opts.KeyringPollInterval)
		ctx := context.Background()
		ring, bootstrapped, err := store.Ensure(ctx)
		if err != nil {
			return nil, nil, "", perr.Wrap(perr.Internal, "load keyring", err)
		}
		if bootstrapped {
			logger.Warn("parsec: bootstrapped a new keyring",
				"path", path, "active_key_id", ring.ActiveID())
		} else {
			logger.Info("parsec: loaded keyring",
				"path", path, "active_key_id", ring.ActiveID())
		}
		return ring, store, path, nil
	}
	ring := auth.NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		return nil, nil, "", perr.Wrap(perr.Internal, "generate ephemeral key", err)
	}
	logger.Warn("parsec: no KeyRing / StateDir provided; generated an ephemeral key. Tokens will not survive restart.",
		"active_key_id", ring.ActiveID())
	return ring, nil, "", nil
}

// WebTransportOptions is re-exported from internal/server so embedders
// don't need to import the internal package for the WT config shape.
type WebTransportOptions struct {
	// Addr is the UDP listen address for the QUIC/HTTP3 listener.
	Addr string
	// TLSCertFile + TLSKeyFile are the TLS cert pair. Required.
	TLSCertFile string
	TLSKeyFile  string
	// AllowedOrigins gates incoming WT requests by Origin header.
	// Empty slice = allow all (dev mode).
	AllowedOrigins []string
}

// Enabled reports whether the WT options describe an active listener.
func (o WebTransportOptions) Enabled() bool { return o.Addr != "" }

// AdminUIOptions controls the embedded operator admin SPA. Off by
// default — when disabled the entire /admin/* tree returns 404 and no
// bytes from the embedded asset bundle reach the response.
type AdminUIOptions struct {
	// Enabled mounts /admin/* with the SPA when true. Operators opt in
	// per deployment; production deployments that don't want the UI
	// leave it off so the asset bundle never enters the response graph.
	Enabled bool
}

// Persistence returns "redis" when the channel registry is Redis-backed,
// or "in-memory" otherwise. Exposed to manifests.
func (p *Parsec) Persistence() string {
	if p.opts.RedisClient != nil {
		return "redis"
	}
	return "in-memory"
}

// DLQ returns the dead-letter queue. Never nil — when no DLQ is configured
// (and no Redis client is present) parsec.New constructs an in-memory
// MemoryDLQ.
func (p *Parsec) DLQ() sinks.DLQ { return p.dlq }

// DLQBackend reports "memory", "redis", or "custom" depending on which
// backend is wired. Exposed in the manifest.
func (p *Parsec) DLQBackend() string { return p.dlqBackend }

// SinkRetryConfig returns the normalized retry config used by PublishOrSink.
// Per-sink overrides are not reflected here — call Options().PerSinkRetry
// directly for that.
func (p *Parsec) SinkRetryConfig() sinks.RetryConfig {
	return p.opts.SinkRetry.Normalize()
}

// Metrics returns the Prometheus collector bundle. Surface code (HTTP
// middleware, sink wrappers, broker hooks) reaches for this rather than
// constructing its own registry.
func (p *Parsec) Metrics() *metrics.Metrics { return p.metrics }

// Tracer returns the active tracer. The default-off path returns a
// no-op tracer that never allocates.
func (p *Parsec) Tracer() trace.Tracer { return p.tracer }

// TracerShutdown flushes any buffered spans and stops the exporter.
// Safe to call when tracing is off (returns nil).
func (p *Parsec) TracerShutdown(ctx context.Context) error {
	if p.tracerShutdown == nil {
		return nil
	}
	return p.tracerShutdown(ctx)
}

// MetricsBearerToken returns the configured bearer for the /metrics
// route. Empty means /metrics is unguarded.
func (p *Parsec) MetricsBearerToken() string { return p.opts.MetricsBearerToken }

// OTLPEndpoint returns the configured OTLP endpoint or "" if tracing
// is off.
func (p *Parsec) OTLPEndpoint() string { return p.opts.OTLPEndpoint }

// AccessLogger returns the slog logger the HTTP surface should use for
// access logs. Falls back to Logger when nothing explicit was set.
func (p *Parsec) AccessLogger() *slog.Logger {
	if p.opts.AccessLogger != nil {
		return p.opts.AccessLogger
	}
	return p.logger
}

// TrustedProxies returns the proxy CIDR list as configured. Empty
// means X-Forwarded-For is ignored.
func (p *Parsec) TrustedProxies() []net.IPNet { return p.opts.TrustedProxies }

// ConfigSource returns the path the TOML config was loaded from, or ""
// when no file was used (flag/env or embedded configuration).
func (p *Parsec) ConfigSource() string { return p.opts.ConfigSource }

// WebTransportOptions returns the WT listener configuration. Used by
// the HTTP surface to decide whether to mount the h3 endpoint.
func (p *Parsec) WebTransportOptions() WebTransportOptions { return p.opts.WebTransport }

// AdminUIOptions returns the embedded admin SPA configuration. Used by
// the HTTP surface to decide whether to mount the /admin/* routes.
func (p *Parsec) AdminUIOptions() AdminUIOptions { return p.opts.AdminUI }

// Region returns the configured region label, or "" when single-region
// (no label) mode is active. Surfaced in the manifest and used as a
// constant metric label by parsec.New.
func (p *Parsec) Region() string { return p.opts.Region }

// Peers returns a defensive copy of the configured peer region URLs.
// Always non-nil-safe: returns nil when no peers were configured. The
// list is purely informational — parsec.New does no validation.
func (p *Parsec) Peers() []string {
	if len(p.opts.Peers) == 0 {
		return nil
	}
	out := make([]string, len(p.opts.Peers))
	copy(out, p.opts.Peers)
	return out
}

// KeyringStore returns the configured KeyRingStore, or nil when the
// instance runs with an explicit/ephemeral ring. Surface code (the CLI
// `keys import` adapter) calls this to push an externally produced
// snapshot through the store so the watch event fires on every node.
func (p *Parsec) KeyringStore() auth.KeyRingStore { return p.keyringStore }

// MetricsPath is the canonical mount point for the Prometheus endpoint.
// Surfaced in the manifest so clients can discover it.
const MetricsPath = "/metrics"

// Broker returns the underlying broker handle.
func (p *Parsec) Broker() *broker.Broker { return p.broker }

// Manager returns the channel lifecycle manager.
func (p *Parsec) Manager() *channels.Manager { return p.manager }

// Sinks returns the registry.
func (p *Parsec) Sinks() *sinks.Registry { return p.opts.Sinks }

// SchemaHandler returns the optional handler mounted at /parsec/schemas,
// or nil when no schema registry was wired.
func (p *Parsec) SchemaHandler() http.Handler { return p.opts.SchemaHandler }

// JWKSHandler returns the optional JWKS handler mounted at
// /parsec/jwks.json. Convenience: when Options.JWKSHandler is nil and
// the live ring carries at least one asymmetric key, parsec.New
// auto-wires auth.JWKSHandler against the ring — operators who add
// an Ed25519 key get a JWKS endpoint for free.
func (p *Parsec) JWKSHandler() http.Handler { return p.opts.JWKSHandler }

// TokenBrokerHandler returns the optional handler mounted under /parsec
// (token, token/delegate, revoke), or nil when no broker was wired.
func (p *Parsec) TokenBrokerHandler() http.Handler { return p.opts.TokenBrokerHandler }

// RevocationStore returns the optional revocation store the subscribe
// authorizer consults on every private channel subscribe. Used by the
// manifest layer to surface the on/off state to operators.
func (p *Parsec) RevocationStore() tokenbroker.RevocationStore { return p.opts.RevocationStore }

// TelemetryHandler returns the optional aggregated /parsec/metrics
// handler, or nil when telemetry aggregation is not wired.
func (p *Parsec) TelemetryHandler() http.Handler { return p.opts.TelemetryHandler }

// Verifier returns the HMAC token verifier. Surface code that needs to
// accept either HMAC or OIDC tokens should prefer CompositeVerifier
// when it is non-nil.
func (p *Parsec) Verifier() *auth.Verifier { return p.verifier }

// CompositeVerifier returns the composed HMAC+OIDC verifier, or nil
// when OIDC is disabled. Surface code that wraps mgmt bearer
// validation should check for non-nil first and fall back to
// Verifier() when nil.
func (p *Parsec) CompositeVerifier() *auth.CompositeVerifier { return p.composite }

// OIDCVerifier returns the OIDC verifier, or nil when OIDC is
// disabled. Exposed primarily for tests + the manifest.
func (p *Parsec) OIDCVerifier() *auth.OIDCVerifier { return p.oidcVerifier }

// OIDCEnabled reports whether an OIDC issuer is wired. Used by the
// manifest layer.
func (p *Parsec) OIDCEnabled() bool { return p.oidcVerifier != nil }

// OIDCIssuer returns the configured OIDC issuer URL, or "" when OIDC
// is disabled. Used by the manifest layer.
func (p *Parsec) OIDCIssuer() string {
	if p.oidcVerifier == nil {
		return ""
	}
	return p.oidcVerifier.Issuer()
}

// Issuer returns the token issuer.
func (p *Parsec) Issuer() *auth.Issuer { return p.issuer }

// KeyRing returns the live ring. Mutations go through the dedicated
// methods (GenerateKey / PromoteKey / RetireKey) so persistence stays in
// sync — surface code should not call ring.Promote directly.
func (p *Parsec) KeyRing() *auth.KeyRing { return p.ring }

// KeyringPath returns the on-disk path of the keyring file, or "" if the
// instance runs with an ephemeral / programmatic ring.
func (p *Parsec) KeyringPath() string { return p.keyringPath }

// Run starts the broker, the sweeper, the manager↔broker event bridge,
// and (when StateDir is set) the keyring file watcher. Blocks until ctx
// is done.
func (p *Parsec) Run(ctx context.Context) error {
	events := make(chan channels.Event, 64)
	p.manager.Subscribe(events)
	go p.runEventBridge(ctx, events)
	go p.manager.RunSweeper(ctx, p.opts.SweepInterval)
	if p.manager.EventBus() != nil {
		go func() {
			if err := p.manager.RunEventBus(ctx); err != nil {
				p.logger.Warn("parsec: event bus exited", "err", err)
			}
		}()
	}
	if p.keyringStore != nil {
		go func() {
			err := p.keyringStore.Watch(ctx, func(fresh *auth.KeyRing) {
				if err := p.ring.LoadSnapshot(fresh.Snapshot()); err != nil {
					p.logger.Warn("parsec: keyring reload error", "err", err)
					return
				}
				p.logger.Info("parsec: keyring reloaded", "active_key_id", fresh.ActiveID())
			})
			if err != nil {
				p.logger.Warn("parsec: keyring watch exited", "err", err)
			}
		}()
	}
	if p.dlq != nil && p.metrics != nil {
		go p.runDLQScrape(ctx)
	}
	return p.broker.Run(ctx)
}

// runDLQScrape periodically calls DLQ.Count(sink) for every registered
// sink and publishes the result to the parsec_dlq_size gauge.
//
// Interval matches the sweep interval — operators tune both with the
// same dial. Errors are logged at debug and never block the loop.
func (p *Parsec) runDLQScrape(ctx context.Context) {
	interval := p.opts.SweepInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, name := range p.opts.Sinks.Names() {
				n, err := p.dlq.Count(ctx, name)
				if err != nil || n < 0 {
					continue
				}
				p.metrics.DLQSize.WithLabelValues(name).Set(float64(n))
			}
		}
	}
}

// runEventBridge translates manager lifecycle events into broker actions.
// Deleted channels kick every connected subscriber; closed public channels
// drain naturally (no kick). The bridge exits when ctx is done.
func (p *Parsec) runEventBridge(ctx context.Context, events <-chan channels.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Kind {
			case channels.EventDeleted:
				p.broker.UnsubscribeAll(ev.Name)
			case channels.EventClosed:
				// public-close = "no new arrivals". Existing subscribers
				// drain at their own pace; no kick.
			case channels.EventOpened:
				// nothing for the broker to do — subscribes will succeed
				// because manager.IsOpen now returns true.
			}
		}
	}
}

// ReloadKeys re-reads the keyring from its backing store. Returns an
// error if no store is configured (i.e. explicit ring or ephemeral).
func (p *Parsec) ReloadKeys() error {
	if p.keyringStore == nil {
		return perr.New(perr.InvalidArgument, "no keyring store configured; pass Options.StateDir or Options.RedisClient")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fresh, err := p.keyringStore.Load(ctx)
	if err != nil {
		return perr.Wrap(perr.Internal, "reload keyring", err)
	}
	return p.ring.LoadSnapshot(fresh.Snapshot())
}

// persistKeyRing writes the current ring to the configured store. No-op
// when no store is configured. Used by key-mutation paths.
func (p *Parsec) persistKeyRing() error {
	if p.keyringStore == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.keyringStore.Save(ctx, p.ring)
}

// GenerateKey mints a new HS256 key (joins the ring as verify-only) and
// persists if StateDir is configured.
func (p *Parsec) GenerateKey() (auth.Key, error) {
	return p.GenerateKeyAlg(auth.AlgHS256)
}

// GenerateKeyAlg mints a new key of the requested algorithm. After
// generation: if the freshly-added key is asymmetric and the embedder
// did not wire an explicit JWKSHandler, parsec auto-installs one so
// the next /parsec/jwks.json hit picks the new public material up.
func (p *Parsec) GenerateKeyAlg(alg auth.Alg) (auth.Key, error) {
	k, err := p.ring.GenerateAlg(alg)
	if err != nil {
		return auth.Key{}, perr.Wrap(perr.InvalidArgument, "generate key", err)
	}
	if err := p.persistKeyRing(); err != nil {
		return auth.Key{}, perr.Wrap(perr.Internal, "persist keyring", err)
	}
	if p.opts.JWKSHandler == nil && k.Alg.IsAsymmetric() {
		p.opts.JWKSHandler = auth.JWKSHandler(p.ring)
	}
	p.metrics.KeyRotationsTotal.WithLabelValues(metrics.KeyActionGenerated).Inc()
	return k, nil
}

// PromoteKey makes id the active signing key.
func (p *Parsec) PromoteKey(id string) error {
	if err := p.ring.Promote(id); err != nil {
		return perr.Wrap(perr.InvalidArgument, "promote key", err)
	}
	if err := p.persistKeyRing(); err != nil {
		return perr.Wrap(perr.Internal, "persist keyring", err)
	}
	p.metrics.KeyRotationsTotal.WithLabelValues(metrics.KeyActionPromoted).Inc()
	return nil
}

// RetireKey removes id from verification.
func (p *Parsec) RetireKey(id string) error {
	if err := p.ring.Retire(id); err != nil {
		return perr.Wrap(perr.InvalidArgument, "retire key", err)
	}
	if err := p.persistKeyRing(); err != nil {
		return perr.Wrap(perr.Internal, "persist keyring", err)
	}
	p.metrics.KeyRotationsTotal.WithLabelValues(metrics.KeyActionRetired).Inc()
	return nil
}

// OpenPublic creates or re-opens a public channel by parsed name. TTL of 0
// uses the manager default. Fossil-delta encoding is off by default;
// enable it after the open call with Manager().SetDelta(name, true).
func (p *Parsec) OpenPublic(name string, ttl time.Duration) (*channels.Channel, error) {
	n, err := channels.ParseName(name)
	if err != nil {
		return nil, err
	}
	return p.manager.OpenPublic(n, ttl)
}

// Credentials returned from CreatePrivate.
type Credentials struct {
	Name           channels.Name
	AccessToken    string
	RefreshToken   string
	AccessExpires  time.Time
	RefreshExpires time.Time
}

// CreatePrivate mints a private channel and returns the access +
// refresh token pair. Pattern grants in scopes are stamped into both
// tokens; pass nil for "exact-match channel only".
func (p *Parsec) CreatePrivate(subjectID, name string, ttl time.Duration, scopes []auth.Scope) (Credentials, error) {
	n, err := channels.ParseName(name)
	if err != nil {
		return Credentials{}, err
	}
	ch, err := p.manager.CreatePrivate(n, ttl)
	if err != nil {
		return Credentials{}, err
	}
	pair, err := p.issuer.IssuePair(subjectID, n.String(), ch.TTL, scopes)
	if err != nil {
		_ = p.manager.Delete(n)
		// Surface validation errors with PARSEC_INVALID_ARGUMENT so RPC
		// callers see the right code (e.g. cross-visibility scope).
		return Credentials{}, perr.Wrap(perr.InvalidArgument, "issue token pair with scopes", err)
	}
	return Credentials{
		Name:           n,
		AccessToken:    pair.AccessToken,
		RefreshToken:   pair.RefreshToken,
		AccessExpires:  pair.AccessExpires,
		RefreshExpires: pair.RefreshExpires,
	}, nil
}

// RefreshResult is what RefreshAccess returns. The RefreshToken /
// RefreshExpires fields are populated on rotation — a token carrying
// a JTI yields a fresh refresh in the same family. Legacy tokens
// (no JTI) leave them zero-valued so older clients keep working.
type RefreshResult struct {
	AccessToken    string
	AccessExpires  time.Time
	RefreshToken   string
	RefreshExpires time.Time
	Rotated        bool
}

// RefreshAccess verifies refreshToken, enforces rotation policy via
// the RefreshStore, and mints a fresh access (and, when the token
// carries a JTI, a fresh refresh in the same family).
//
// Rotation rules:
//
//  1. Tokens without JTI are legacy: mint a new access only. The
//     store is untouched.
//  2. Tokens with JTI consult the store. A revoked family or an
//     already-redeemed JTI returns PARSEC_AUTH_DENIED. A reused JTI
//     additionally revokes the family so subsequent siblings fail
//     even before the JTI check.
//  3. On success, both tokens are reissued: the new refresh shares
//     the old FID, a fresh JTI, and refresh TTL bounded by the
//     channel TTL and MaxRefreshTTL.
func (p *Parsec) RefreshAccess(refreshToken string) (RefreshResult, error) {
	return p.RefreshAccessCtx(context.Background(), refreshToken)
}

// RefreshAccessCtx is RefreshAccess with an explicit context. The
// context is forwarded to the RefreshStore so Redis-backed stores
// pick up the caller's cancellation + deadline. Most callers should
// use RefreshAccess; this overload exists for the RPC adapter.
func (p *Parsec) RefreshAccessCtx(ctx context.Context, refreshToken string) (RefreshResult, error) {
	claims, err := p.verifier.Verify(refreshToken, auth.TypeRefresh)
	if err != nil {
		return RefreshResult{}, auth.MapErr(err)
	}
	if len(claims.Chs) != 1 {
		return RefreshResult{}, perr.New(perr.AuthDenied, "refresh token has no channel scope")
	}
	name, err := channels.ParseName(claims.Chs[0])
	if err != nil {
		return RefreshResult{}, perr.Wrap(perr.AuthDenied, "refresh channel invalid", err)
	}
	ch, err := p.manager.Get(name)
	if err != nil {
		return RefreshResult{}, err
	}
	if ch.State != channels.StateOpen {
		return RefreshResult{}, perr.New(perr.ChannelClosed, "channel is not open")
	}

	// Legacy token (no JTI): no store interaction, mint access only.
	if claims.JTI == "" {
		access, exp, err := p.issuer.IssueAccess(claims.Sub, claims.Chs[0], claims.ExpiresAt(), claims.Scopes)
		if err != nil {
			return RefreshResult{}, perr.Wrap(perr.AuthDenied, "issue access from refresh", err)
		}
		p.recordRefresh("legacy")
		return RefreshResult{AccessToken: access, AccessExpires: exp}, nil
	}

	// Rotated path: family revocation gate first, then atomic JTI
	// redemption. The order matters — a revoked family must be
	// rejected even if the JTI itself is still fresh.
	if p.refreshStore != nil {
		revoked, err := p.refreshStore.IsFamilyRevoked(ctx, claims.FID)
		if err != nil {
			return RefreshResult{}, perr.Wrap(perr.Internal, "refresh store family check", err)
		}
		if revoked {
			p.recordRefresh("family_revoked")
			return RefreshResult{}, perr.New(perr.AuthDenied, "refresh family revoked")
		}
		if err := p.refreshStore.MarkRedeemed(ctx, claims.JTI, claims.ExpiresAt()); err != nil {
			if errors.Is(err, auth.ErrRefreshReused) {
				// Reuse trips the family kill-switch. Errors from
				// RevokeFamily are surfaced — losing it would leave
				// a known-leaked family alive past this call.
				if rerr := p.refreshStore.RevokeFamily(ctx, claims.FID, claims.ExpiresAt()); rerr != nil {
					return RefreshResult{}, perr.Wrap(perr.Internal, "revoke leaked family", rerr)
				}
				p.recordRefresh("reused")
				return RefreshResult{}, perr.New(perr.AuthDenied, "refresh token already redeemed; family revoked")
			}
			return RefreshResult{}, perr.Wrap(perr.Internal, "refresh store redeem", err)
		}
	}

	pair, err := p.issuer.IssueRotatedPair(claims.Sub, claims.Chs[0], claims.FID, ch.TTL, claims.Scopes)
	if err != nil {
		return RefreshResult{}, perr.Wrap(perr.AuthDenied, "issue rotated pair", err)
	}
	p.recordRefresh("rotated")
	return RefreshResult{
		AccessToken:    pair.AccessToken,
		AccessExpires:  pair.AccessExpires,
		RefreshToken:   pair.RefreshToken,
		RefreshExpires: pair.RefreshExpires,
		Rotated:        true,
	}, nil
}

// recordRefresh increments the rotation outcome metric. No-op when
// the metric collector is not present (e.g. test instances that
// share a zero Metrics).
func (p *Parsec) recordRefresh(result string) {
	if p.metrics == nil || p.metrics.RefreshRotations == nil {
		return
	}
	p.metrics.RefreshRotations.WithLabelValues(result).Inc()
}

// Publish ships data to a managed channel.
func (p *Parsec) Publish(ctx context.Context, name string, data []byte) (broker.PublishResult, error) {
	n, err := channels.ParseName(name)
	if err != nil {
		return broker.PublishResult{}, err
	}
	ch, err := p.manager.Get(n)
	if err != nil {
		return broker.PublishResult{}, err
	}
	vis := metrics.VisibilityLabel(string(n.Visibility))
	start := time.Now()
	res, err := p.broker.Publish(ctx, n, data, ch.DeltaEnabled)
	p.metrics.PublishDuration.WithLabelValues(vis).Observe(time.Since(start).Seconds())
	if err != nil {
		p.metrics.PublishesTotal.WithLabelValues(vis, metrics.ResultFailure).Inc()
		return res, err
	}
	p.metrics.PublishesTotal.WithLabelValues(vis, metrics.ResultSuccess).Inc()
	_ = p.manager.Touch(n)
	return res, nil
}

// PublishOrSink publishes to ch if there is at least one live subscriber,
// otherwise falls back to the named sink.
func (p *Parsec) PublishOrSink(ctx context.Context, name string, data []byte, sinkName string, to sinks.Recipient, msg sinks.Message) (bool, error) {
	n, err := channels.ParseName(name)
	if err != nil {
		return false, err
	}
	presence, err := p.broker.Presence(ctx, n)
	if err != nil {
		return false, err
	}
	if presence > 0 {
		ch, err := p.manager.Get(n)
		delta := err == nil && ch.DeltaEnabled
		if _, err := p.broker.Publish(ctx, n, data, delta); err != nil {
			return false, err
		}
		_ = p.manager.Touch(n)
		return false, nil
	}
	s := p.opts.Sinks.Get(sinkName)
	if s == nil {
		return false, perr.New(perr.SinkConfig, "unknown sink: "+sinkName)
	}
	return true, s.Send(ctx, to, msg)
}

// Re-exports for embedders.
type (
	Channel     = channels.Channel
	ChannelName = channels.Name
	Sink        = sinks.Sink
	Message     = sinks.Message
)

var _ = errors.New // keep stdlib errors imported for any future surface
