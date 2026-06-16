package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// AccessLogOptions configures the access-log middleware.
type AccessLogOptions struct {
	// Logger receives the JSON-shaped INFO line per request. Required;
	// access-log wiring is skipped when nil.
	Logger *slog.Logger
	// TrustedProxies is the list of CIDRs/IPs Parsec will honor an
	// X-Forwarded-For chain from. When the immediate remote is not in
	// this list, X-Forwarded-For is ignored and the TCP peer is logged.
	TrustedProxies []net.IPNet
	// Region is the operator-configured region label. When non-empty
	// it is stamped on every access-log line so operators can grep /
	// slice their multi-region logs. Empty omits the field entirely so
	// single-region logs stay lean.
	Region string
}

// AccessLog returns an HTTP middleware that emits one slog INFO line per
// request and one WARN line per auth failure recorded via LogAuthFailure.
//
// Fields:
//   - method, path, status, duration_ms
//   - remote_addr (X-Forwarded-For aware via TrustedProxies)
//   - request_id (X-Request-ID; auto-generated if missing)
//   - bearer_subject (best-effort: base64-decoded JWT sub, NOT verified)
//   - trace_id (from the active OTel span if any; "" otherwise)
//
// Token contents are NEVER logged. The bearer_subject value is
// best-effort decoded from the public JWT payload purely for operational
// correlation, not authorization.
func AccessLog(opts AccessLogOptions, next http.Handler) http.Handler {
	if opts.Logger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
		}
		ctx := withRequestID(r.Context(), requestID)
		ctx = withAccessLogger(ctx, opts.Logger)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", requestID)

		rec := &accessRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		remote := remoteAddr(r, opts.TrustedProxies)
		traceID := ""
		if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
			traceID = sc.TraceID().String()
		}
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("remote_addr", remote),
			slog.String("request_id", requestID),
			slog.String("bearer_subject", bearerSubject(r.Header.Get("Authorization"))),
			slog.String("trace_id", traceID),
		}
		if opts.Region != "" {
			attrs = append(attrs, slog.String("region", opts.Region))
		}
		opts.Logger.LogAttrs(r.Context(), slog.LevelInfo, "http_request", attrs...)
	})
}

// LogAuthFailure emits a WARN log for a token verification failure. Code
// is the PARSEC_AUTH_* code from the verifier. The token itself is never
// included.
func LogAuthFailure(ctx context.Context, code string) {
	lg := accessLoggerFromContext(ctx)
	if lg == nil {
		return
	}
	lg.LogAttrs(ctx, slog.LevelWarn, "auth_failure",
		slog.String("code", code),
		slog.String("request_id", requestIDFromContext(ctx)),
	)
}

// accessRecorder mirrors metrics.statusRecorder; we keep it private to
// the server package because access logging needs the same capture.
type accessRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (a *accessRecorder) WriteHeader(code int) {
	if !a.wroteHeader {
		a.status = code
		a.wroteHeader = true
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *accessRecorder) Write(b []byte) (int, error) {
	if !a.wroteHeader {
		a.wroteHeader = true
	}
	return a.ResponseWriter.Write(b)
}

func (a *accessRecorder) Flush() {
	if f, ok := a.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer so the centrifuge WebSocket
// handler can upgrade the connection. Without this the middleware
// silently breaks every /connection/websocket request with a 500.
func (a *accessRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := a.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// generateRequestID returns a short, URL-safe random ID. 12 bytes of
// random = 24 hex characters: collision-resistant for any sane request
// volume and short enough to grep cleanly.
func generateRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Best-effort fallback: a fixed marker so the field is never
		// empty. Production should never hit this path — rand.Read on
		// modern Go cannot fail.
		return "req_unknown"
	}
	return hex.EncodeToString(b[:])
}

// remoteAddr returns the operator-visible remote address. When the
// immediate TCP peer is in trustedProxies, the leftmost X-Forwarded-For
// entry is preferred; otherwise the TCP peer wins.
func remoteAddr(r *http.Request, trusted []net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(trusted) == 0 {
		return host
	}
	peer := net.ParseIP(host)
	if peer == nil || !proxyTrusted(peer, trusted) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	// Leftmost entry = original client. Subsequent entries are the
	// proxy chain; trusting them blindly would let any proxy spoof a
	// client. We only honor the leftmost when the immediate peer is in
	// our trusted set.
	parts := strings.Split(xff, ",")
	if len(parts) == 0 {
		return host
	}
	candidate := strings.TrimSpace(parts[0])
	if candidate == "" {
		return host
	}
	return candidate
}

// proxyTrusted reports whether ip lies in any of the trusted CIDRs.
func proxyTrusted(ip net.IP, trusted []net.IPNet) bool {
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// bearerSubject best-effort decodes the "sub" claim from a JWT in the
// Authorization header. Returns "" on any failure. THIS DOES NOT
// VERIFY the signature — the value is for log correlation only.
func bearerSubject(h string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	token := strings.TrimSpace(h[len(prefix):])
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Sub
}

// ctxKey is the private type for context keys. Two distinct values keep
// our keys from colliding with other packages' context keys.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyAccessLogger
)

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestID returns the request ID for ctx, or "" if none was set.
func RequestID(ctx context.Context) string { return requestIDFromContext(ctx) }

func requestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

func withAccessLogger(ctx context.Context, lg *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyAccessLogger, lg)
}

func accessLoggerFromContext(ctx context.Context) *slog.Logger {
	v, _ := ctx.Value(ctxKeyAccessLogger).(*slog.Logger)
	return v
}
