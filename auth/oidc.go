// OIDC bridge for management authentication. The HMAC verifier in
// verifier.go remains the primary path; an OIDCVerifier sits beside
// it (composed via CompositeVerifier) so corporate IdP-issued ID
// tokens can authenticate operators without provisioning a separate
// HMAC mgmt token.
//
// The OIDC verifier does NOT introduce a new token type — incoming
// ID tokens are translated into a synthetic auth.Claims with
// Typ=TypeMgmt so downstream code (rate limiter, scope authorizer,
// access log) treats both kinds uniformly. The HMAC token format is
// untouched by this file.

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// OIDCConfig captures the operator-supplied configuration for an OIDC
// identity provider. Empty Issuer means "OIDC is disabled" — the
// composite verifier short-circuits past it.
type OIDCConfig struct {
	// Issuer is the IdP's OpenID issuer URL (e.g.
	// "https://accounts.google.com"). NewOIDCVerifier fetches
	// <Issuer>/.well-known/openid-configuration to discover the
	// JWKS endpoint and supported algorithms.
	Issuer string

	// Audience is the expected `aud` claim. Set to the parsec
	// deployment's client identifier (e.g. "parsec-prod"). If empty
	// the verifier rejects every token (defensive — every IdP
	// requires an audience).
	Audience string

	// SubjectClaim names the ID-token claim to copy into the
	// synthetic Claims.Sub field. Defaults to "sub" when empty.
	// Operators frequently set this to "email" so the access log
	// records the operator's address.
	SubjectClaim string

	// ScopesClaim names the ID-token claim that carries the
	// operator's group memberships. Defaults to "groups" when
	// empty. The claim is expected to be a JSON array of strings.
	ScopesClaim string

	// Grants maps IdP group names onto parsec scope patterns. When
	// an incoming token's ScopesClaim contains a group listed here,
	// the matching grant's Scope + Verbs is added to the synthetic
	// Claims.Scopes set. Multiple grants may match a single token.
	Grants []OIDCGrant

	// Clock overrides time.Now for verification, primarily for
	// tests. nil = time.Now.
	Clock func() time.Time
}

// OIDCGrant maps one IdP group name onto one parsec scope. When the
// incoming ID token's group list contains IfGroup, the synthetic
// Claims gains a Scope with Pattern=Scope and Verbs=Verbs.
type OIDCGrant struct {
	// IfGroup is the literal group name to match against the
	// token's ScopesClaim list. Comparison is byte-exact — no
	// wildcards, no case folding.
	IfGroup string

	// Scope is the channel-name pattern to grant (parsec scope
	// grammar — see channels.ParsePattern).
	Scope string

	// Verbs is the set of actions the matching operator may take
	// on channels covered by Scope.
	Verbs []Verb
}

// OIDCVerifier wraps go-oidc's provider + id-token verifier behind a
// parsec-friendly Verify call that returns synthetic auth.Claims.
//
// One OIDCVerifier is constructed per parsec.New (issuer discovery
// happens at boot, NOT per request). The underlying *oidc.IDTokenVerifier
// is safe for concurrent use, so a single OIDCVerifier services every
// inbound request.
type OIDCVerifier struct {
	cfg      OIDCConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
}

// NewOIDCVerifier discovers the issuer's OpenID configuration and
// returns a verifier ready to validate ID tokens. The ctx is used for
// the discovery roundtrip only — once NewOIDCVerifier returns, the
// verifier is decoupled from ctx and can outlive it.
//
// Returns an error when cfg.Issuer is empty (callers should construct
// no verifier in that case), when discovery fails, or when cfg.Audience
// is empty.
func NewOIDCVerifier(ctx context.Context, cfg OIDCConfig) (*OIDCVerifier, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("auth: OIDCConfig.Issuer is required")
	}
	if cfg.Audience == "" {
		return nil, errors.New("auth: OIDCConfig.Audience is required")
	}
	if cfg.SubjectClaim == "" {
		cfg.SubjectClaim = "sub"
	}
	if cfg.ScopesClaim == "" {
		cfg.ScopesClaim = "groups"
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery %s: %w", cfg.Issuer, err)
	}
	verifier := provider.Verifier(&oidc.Config{
		ClientID: cfg.Audience,
		Now:      cfg.Clock,
	})
	return &OIDCVerifier{cfg: cfg, provider: provider, verifier: verifier}, nil
}

// Issuer reports the configured issuer URL. Exposed for the manifest.
func (v *OIDCVerifier) Issuer() string { return v.cfg.Issuer }

// Audience reports the configured audience. Exposed for the manifest
// and tests.
func (v *OIDCVerifier) Audience() string { return v.cfg.Audience }

// Verify validates token against the configured IdP (signature, exp,
// iat, aud, iss) and translates the result into a synthetic mgmt
// Claims. Returns one of the auth sentinel errors on failure so the
// composite verifier and the HTTP middleware can map to PARSEC_AUTH_*.
//
// The returned Claims always carries Typ=TypeMgmt. OIDC IdPs do not
// issue refresh / access tokens in the parsec sense; clients hit
// `parsec login oidc` once per session and present the resulting ID
// token as a mgmt bearer.
func (v *OIDCVerifier) Verify(ctx context.Context, token string) (Claims, error) {
	idTok, err := v.verifier.Verify(ctx, token)
	if err != nil {
		return Claims{}, translateOIDCError(err)
	}
	// Decode the raw claims to pluck subject + groups by configured
	// claim name. go-oidc's IDToken.Claims hands back the full
	// payload as JSON; we re-unmarshal into a map so the configured
	// claim names work without a typed struct.
	var raw map[string]any
	if err := idTok.Claims(&raw); err != nil {
		return Claims{}, ErrMalformedToken
	}
	sub, ok := raw[v.cfg.SubjectClaim].(string)
	if !ok || sub == "" {
		// Fall back to the standard `sub` claim — some IdPs only
		// honor SubjectClaim opt-in via additional scopes (e.g.
		// Google needs the `email` scope before `email` is present).
		sub = idTok.Subject
	}
	if sub == "" {
		return Claims{}, ErrMalformedToken
	}
	groups := extractStringList(raw[v.cfg.ScopesClaim])
	scopes := mapGrants(v.cfg.Grants, groups)
	return Claims{
		Sub:    sub,
		Typ:    TypeMgmt,
		Iat:    idTok.IssuedAt.Unix(),
		Exp:    idTok.Expiry.Unix(),
		Scopes: scopes,
	}, nil
}

// translateOIDCError maps go-oidc's error text into the auth package's
// sentinel errors so callers can errors.Is against the same set they
// use for HMAC failures. go-oidc does not export typed errors for
// these conditions, so we string-match — defensive but bounded.
func translateOIDCError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case containsAny(msg, "expired", "token is expired", "exp not satisfied"):
		return ErrExpired
	case containsAny(msg, "before issued at", "iat", "not yet valid", "used before issued"):
		return ErrNotYetValid
	case containsAny(msg, "expected audience", "aud"):
		return ErrTypeMismatch
	case containsAny(msg, "issuer did not match", "iss"):
		return ErrTypeMismatch
	case containsAny(msg, "failed to verify signature", "signature"):
		return ErrInvalidSignature
	case containsAny(msg, "unsupported", "alg"):
		return ErrUnsupportedAlg
	default:
		return ErrMalformedToken
	}
}

// containsAny is a tiny stdlib-free helper to avoid importing strings
// in this file twice.
func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if indexOf(s, n) >= 0 {
			return true
		}
	}
	return false
}

// indexOf returns the first index of needle in s, or -1.
func indexOf(s, needle string) int {
	if needle == "" {
		return 0
	}
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// extractStringList normalizes the value of the scopes claim into a
// flat []string. The IdP may serialize it as a JSON array of strings,
// a single string, or a space-delimited string (OAuth2 convention).
func extractStringList(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return append([]string(nil), t...)
	case string:
		if t == "" {
			return nil
		}
		// Space-delimited fallback for IdPs that send a single
		// `scope` string. We do NOT split on commas because group
		// names legitimately contain commas in some directories.
		var out []string
		start := 0
		for i := 0; i <= len(t); i++ {
			if i == len(t) || t[i] == ' ' {
				if i > start {
					out = append(out, t[start:i])
				}
				start = i + 1
			}
		}
		return out
	case json.RawMessage:
		var arr []string
		if err := json.Unmarshal(t, &arr); err == nil {
			return arr
		}
		var one string
		if err := json.Unmarshal(t, &one); err == nil && one != "" {
			return []string{one}
		}
		return nil
	}
	return nil
}

// mapGrants returns the scopes that match any of the operator's
// groups. An operator with no matching groups gets an empty scope set
// — the synthetic Claims still has Typ=TypeMgmt, so the bearer
// middleware accepts the token, but downstream surface code that
// checks Authorizes (the broker subscribe authorizer) rejects every
// channel. Operators with no group mapping effectively get a
// read-only manifest-only session, which is the safe default.
func mapGrants(grants []OIDCGrant, groups []string) []Scope {
	if len(grants) == 0 || len(groups) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		have[g] = struct{}{}
	}
	var out []Scope
	for _, g := range grants {
		if _, ok := have[g.IfGroup]; !ok {
			continue
		}
		if g.Scope == "" || len(g.Verbs) == 0 {
			continue
		}
		verbs := append([]Verb(nil), g.Verbs...)
		out = append(out, Scope{Pattern: g.Scope, Verbs: verbs})
	}
	return out
}
