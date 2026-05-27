package parsec

import (
	"context"

	"github.com/frankbardon/parsec/auth"
)

// Context-key helpers used by surface code (HTTP middleware) to plant
// per-request identity into the call chain, and by business-logic code
// (service.Service) to read it back out for rate-limit gating.
//
// Defined at the library level so packages on either side can share the
// API without creating an import cycle.

type ctxKey int

const (
	ctxKeySubject ctxKey = iota
	ctxKeyClaims
	ctxKeyRemoteIP
)

// WithSubject returns a new context that carries the authenticated
// bearer subject (the sub claim). Surface code calls this from the
// bearer middleware after validation succeeds.
func WithSubject(ctx context.Context, sub string) context.Context {
	if sub == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeySubject, sub)
}

// SubjectFromContext returns the bearer subject planted by WithSubject,
// or "" when no bearer was presented (e.g. RefreshToken's body
// authentication, or tests with bearer enforcement disabled).
func SubjectFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeySubject).(string); ok {
		return v
	}
	return ""
}

// WithClaims returns a new context that carries the validated claims.
// Surface code calls this so downstream rate-limit gates can read the
// optional rl override.
func WithClaims(ctx context.Context, c *auth.Claims) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyClaims, c)
}

// ClaimsFromContext returns the claims planted by WithClaims, or nil.
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	if v, ok := ctx.Value(ctxKeyClaims).(*auth.Claims); ok {
		return v
	}
	return nil
}

// WithRemoteIP returns a new context that carries the request's remote
// IP (host portion of r.RemoteAddr). Used by gates that key off IP
// instead of subject — typically RefreshToken which has no mgmt bearer.
func WithRemoteIP(ctx context.Context, ip string) context.Context {
	if ip == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyRemoteIP, ip)
}

// RemoteIPFromContext returns the IP planted by WithRemoteIP, or "".
func RemoteIPFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRemoteIP).(string); ok {
		return v
	}
	return ""
}
