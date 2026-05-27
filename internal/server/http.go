// Package server mounts the parsec HTTP surface: the centrifuge websocket
// transport, the rpc Twirp-JSON handler, an SSE fallback for the CLI
// `subscribe` probe, and /healthz.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/centrifugal/centrifuge"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/internal/admin"
	"github.com/frankbardon/parsec/internal/metrics"
	"github.com/frankbardon/parsec/rpc"
	"github.com/frankbardon/parsec/service"
)

// BearerValidator decides whether the incoming Authorization: Bearer token
// is acceptable. New injects one via the bearer middleware; nil means "no
// auth required" and is intended for tests only.
type BearerValidator func(token string) error

// SubjectExtractor returns the token's sub claim (and any per-token
// rate-limit override) given the raw bearer string. Optional — when
// nil the bearer middleware does not stamp a subject in the request
// context, and downstream rate-limit gates fall back to the remote
// IP key.
type SubjectExtractor func(token string) (subject string, claims *auth.Claims)

// New returns the composed http.Handler. p must be running (or about to be);
// svc is the business-logic layer; logger may be nil. validate is the
// bearer-token validator for the RPC surface; pass nil to disable bearer
// auth (tests only).
func New(p *parsec.Parsec, svc *service.Service, logger *slog.Logger, validate BearerValidator) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()

	// Twirp management surface (generated). The generated handler does not
	// enforce bearer tokens — we wrap it in a bearer middleware that skips
	// the public methods (Manifest, RefreshToken).
	twirpHandler := rpc.NewParsecServiceServer(service.NewRPCAdapter(svc))
	extract := MgmtExtractor(p)
	mux.Handle(rpc.ParsecServicePathPrefix, ipMiddleware(bearerMiddleware(twirpHandler, validate, extract)))

	// Centrifuge websocket transport.
	wsHandler := centrifuge.NewWebsocketHandler(p.Broker().Node(), centrifuge.WebsocketConfig{})
	mux.Handle("/connection/websocket", wsHandler)

	// Optional WebTransport (HTTP/3) handler.
	if wt := p.WebTransportOptions(); wt.Enabled() {
		wtOpts := WebTransportOptions{
			Addr:           wt.Addr,
			TLSCertFile:    wt.TLSCertFile,
			TLSKeyFile:     wt.TLSKeyFile,
			AllowedOrigins: wt.AllowedOrigins,
		}
		wtHandler, err := MountWebTransport(p.Broker().Node(), wtOpts, logger)
		if err != nil {
			logger.Warn("webtransport disabled (mount failed)", "err", err)
		} else {
			mux.Handle("/connection/webtransport", wtHandler)
			logger.Info("webtransport enabled", "addr", wt.Addr)
		}
	}

	// SSE probe used by the CLI `parsec subscribe`.
	mux.Handle("/sse", &sseHandler{p: p, logger: logger})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(svc.Manifest(r.Context()))
	})

	// Embedded admin SPA. Opt-in via Options.AdminUI.Enabled — when
	// disabled the routes are simply absent and every /admin/* hit
	// returns 404 via ServeMux's default behaviour. All API calls the
	// SPA makes go through the bearer-gated Twirp surface; the static
	// asset routes here serve HTML/JS/CSS only, so they don't enforce
	// a bearer themselves (the client-side guard redirects to the
	// login form when no token is in sessionStorage).
	if p.AdminUIOptions().Enabled {
		mux.Handle("/admin", admin.Handler())
		mux.Handle("/admin/", admin.Handler())
		logger.Info("admin ui enabled", "mount", "/admin")
	}

	// Optional upgrade-spec surfaces. Each is mounted only when the
	// embedder supplied a handler via Options.
	if h := p.SchemaHandler(); h != nil {
		mux.Handle("/parsec/schemas", h)
		mux.Handle("/parsec/schemas/", h)
		logger.Info("schema registry mounted", "path", "/parsec/schemas")
	}
	if h := p.TokenBrokerHandler(); h != nil {
		mux.Handle("/parsec/", http.StripPrefix("/parsec", h))
		logger.Info("token broker mounted", "prefix", "/parsec")
	}
	if h := p.TelemetryHandler(); h != nil {
		mux.Handle("/parsec/metrics", h)
		logger.Info("telemetry mounted", "path", "/parsec/metrics")
	}

	// Prometheus scrape endpoint. Bearer-gated when MetricsBearerToken
	// is set; unguarded otherwise (operator firewalls it at the network
	// layer or binds /metrics to a private interface).
	m := p.Metrics()
	if m != nil {
		promHandler := promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{
			Registry: m.Registry,
		})
		mux.Handle(parsec.MetricsPath, metricsBearerMiddleware(promHandler, p.MetricsBearerToken()))
	}

	// Compose middleware: access log is outermost so its duration_ms
	// covers every other middleware, including metrics recording.
	var handler http.Handler = mux
	if m != nil {
		handler = metrics.Middleware(m, handler)
	}
	handler = AccessLog(AccessLogOptions{
		Logger:         p.AccessLogger(),
		TrustedProxies: p.TrustedProxies(),
		Region:         p.Region(),
	}, handler)
	return handler
}

// metricsBearerMiddleware gates the /metrics endpoint. When expected is
// empty the endpoint is unguarded. The comparison is a plain string
// match — /metrics is a deployment secret, not a JWT.
func metricsBearerMiddleware(next http.Handler, expected string) http.Handler {
	if expected == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := bearerFromHeader(r.Header.Get("Authorization"))
		if got == "" || got != expected {
			w.Header().Set("WWW-Authenticate", `Bearer realm="parsec-metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authCode maps a verifier error string back to a PARSEC_AUTH_* code so
// the access-log WARN line carries the operator-facing reason without
// leaking error text or token contents.
func authCode(err error) string {
	if err == nil {
		return ""
	}
	switch err.Error() {
	case auth.ErrExpired.Error():
		return "PARSEC_AUTH_EXPIRED"
	case auth.ErrInvalidSignature.Error():
		return "PARSEC_AUTH_INVALID_SIGNATURE"
	case auth.ErrUnsupportedAlg.Error():
		return "PARSEC_AUTH_UNSUPPORTED_ALG"
	case auth.ErrMalformedToken.Error():
		return "PARSEC_AUTH_MALFORMED"
	case auth.ErrTypeMismatch.Error():
		return "PARSEC_AUTH_TYPE_MISMATCH"
	case auth.ErrNotYetValid.Error():
		return "PARSEC_AUTH_NOT_YET_VALID"
	default:
		return "PARSEC_AUTH_DENIED"
	}
}

// MgmtValidator returns a BearerValidator that accepts any valid mgmt
// token signed by the parsec instance's secret. When the parsec
// instance has an OIDC verifier wired (Options.OIDCConfig non-nil),
// the validator also accepts ID tokens from the configured issuer —
// HMAC is tried first, OIDC is the fallback. A deployment without
// OIDCConfig only accepts HMAC tokens.
func MgmtValidator(p *parsec.Parsec) BearerValidator {
	if cv := p.CompositeVerifier(); cv != nil {
		return func(token string) error {
			_, err := cv.Verify(context.Background(), token, auth.TypeMgmt)
			return err
		}
	}
	v := p.Verifier()
	return func(token string) error {
		_, err := v.Verify(token, auth.TypeMgmt)
		return err
	}
}

// MgmtExtractor returns a SubjectExtractor that decodes the mgmt bearer's
// claims so the bearer middleware can stamp the subject (and any
// per-token rate-limit override) into the request context. Verification
// errors are swallowed — validate runs first; if validate accepted, the
// claims are guaranteed to parse. When OIDC is wired the extractor
// pulls claims via the composite verifier so OIDC-issued bearers also
// stamp a subject.
func MgmtExtractor(p *parsec.Parsec) SubjectExtractor {
	if cv := p.CompositeVerifier(); cv != nil {
		return func(token string) (string, *auth.Claims) {
			c, err := cv.Verify(context.Background(), token, auth.TypeMgmt)
			if err != nil {
				return "", nil
			}
			return c.Sub, &c
		}
	}
	v := p.Verifier()
	return func(token string) (string, *auth.Claims) {
		c, err := v.Verify(token, auth.TypeMgmt)
		if err != nil {
			return "", nil
		}
		return c.Sub, &c
	}
}

// publicTwirpMethods is the set of RPC paths that bypass bearer enforcement.
// Manifest is read-only; RefreshToken validates its own in-body refresh
// token instead of a mgmt bearer.
var publicTwirpMethods = map[string]bool{
	rpc.ParsecServicePathPrefix + "Manifest":     true,
	rpc.ParsecServicePathPrefix + "RefreshToken": true,
}

// bearerMiddleware enforces an Authorization: Bearer <token> header for any
// twirp method that isn't in publicTwirpMethods. On failure it writes a
// Twirp-shaped JSON error envelope and short-circuits the request. validate
// may be nil for tests, in which case all requests are passed through.
//
// When extract is non-nil and validate accepts the token, the resolved
// subject and claims are stamped into the request context so downstream
// rate-limit gates can read them.
func bearerMiddleware(next http.Handler, validate BearerValidator, extract SubjectExtractor) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if validate == nil || publicTwirpMethods[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		bearer := bearerFromHeader(r.Header.Get("Authorization"))
		if bearer == "" {
			LogAuthFailure(r.Context(), "PARSEC_AUTH_MISSING")
			writeTwirpUnauthenticated(w, "missing bearer token")
			return
		}
		if err := validate(bearer); err != nil {
			LogAuthFailure(r.Context(), authCode(err))
			writeTwirpUnauthenticated(w, err.Error())
			return
		}
		if extract != nil {
			sub, claims := extract(bearer)
			if sub != "" {
				ctx := parsec.WithSubject(r.Context(), sub)
				if claims != nil {
					ctx = parsec.WithClaims(ctx, claims)
				}
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ipMiddleware stamps the remote IP into the request context. The result
// is the parsed host portion of r.RemoteAddr; X-Forwarded-For is not
// trusted here — the parsec.Options.TrustedProxies machinery lives at
// the parsec library level for now and operators who terminate TLS in
// front of parsec should run the upstream rate limit there.
func ipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ctx := parsec.WithRemoteIP(r.Context(), host)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerFromHeader extracts the token from "Bearer <token>". Returns ""
// if the header is missing or malformed.
func bearerFromHeader(h string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// writeTwirpUnauthenticated writes a Twirp v8 JSON error envelope with
// code=unauthenticated and HTTP status 401. The shape matches what the
// generated server writes for native twirp.NewError(twirp.Unauthenticated,
// ...) errors so clients can decode uniformly.
func writeTwirpUnauthenticated(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code": "unauthenticated",
		"msg":  msg,
	})
}
