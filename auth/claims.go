// Package auth issues and verifies the HMAC-SHA256 JWTs that gate access to
// Parsec. There are three token types, distinguished by the typ claim:
//
//   - access  — short-lived, used to connect over the websocket and to
//               subscribe to listed private channels
//   - refresh — exchanged at the RefreshToken RPC for a fresh access token
//   - mgmt    — operator token presented as Authorization: Bearer on the
//               management RPC surface
//
// The wire format is the standard JWT compact serialization
// (base64url(header).base64url(claims).base64url(hmac)), but Parsec uses a
// fixed header — alg=HS256, typ=JWT — and refuses any other.
//
// No JWT library: the implementation is ~150 lines of stdlib crypto/hmac and
// crypto/sha256.
package auth

import (
	"errors"
	"time"

	"github.com/frankbardon/parsec/channels"
	"github.com/frankbardon/parsec/ratelimit"
)

// Type is the JWT typ-extension claim Parsec uses to disambiguate token
// purpose. The standard JWT typ header stays "JWT"; the discriminator
// lives in the payload's "typ" field.
type Type string

const (
	TypeAccess  Type = "access"
	TypeRefresh Type = "refresh"
	TypeMgmt    Type = "mgmt"
)

// Valid returns nil if t is one of the recognized token types.
func (t Type) Valid() error {
	switch t {
	case TypeAccess, TypeRefresh, TypeMgmt:
		return nil
	default:
		return errors.New("unknown token type")
	}
}

// Claims is the Parsec payload. Fields use JWT-conventional short names so
// the wire shape is compact.
type Claims struct {
	// Sub is the subject — user id for client tokens, operator id for mgmt.
	Sub string `json:"sub,omitempty"`
	// Typ is the Parsec token type discriminator.
	Typ Type `json:"typ"`
	// Chs is the list of channels this token authorizes. Empty means
	// "no client-channel authorization." For mgmt tokens it is ignored.
	Chs []string `json:"chs,omitempty"`
	// Iat is the issued-at time (Unix seconds).
	Iat int64 `json:"iat"`
	// Exp is the expiration time (Unix seconds).
	Exp int64 `json:"exp"`
	// RateLimitOverride, when non-nil, overrides the server's default
	// per-subject rate limit for this token. Useful to hand a noisy
	// integration a tighter budget without touching server config. The
	// claim is private to Parsec (no JWT-RFC equivalent); absence means
	// "use the operator-configured default."
	RateLimitOverride *ratelimit.Limit `json:"rl,omitempty"`
	// Scopes is the pattern-based grant set. Each Scope binds a
	// channel-name Pattern to a set of Verbs (subscribe, publish,
	// manage). Empty means "no pattern grants" — only the Chs exact
	// list applies. A token carrying both Chs and Scopes is authorized
	// for the union.
	Scopes []Scope `json:"scopes,omitempty"`
}

// IssuedAt returns Iat as a time.Time.
func (c Claims) IssuedAt() time.Time { return time.Unix(c.Iat, 0) }

// ExpiresAt returns Exp as a time.Time.
func (c Claims) ExpiresAt() time.Time { return time.Unix(c.Exp, 0) }

// Authorizes reports whether the holder is granted verb v on the named
// channel. The check unions two grant sources, with deny-wins
// precedence applied uniformly:
//
//   - Chs (exact match): any name in c.Chs authorizes every verb.
//   - Scopes (pattern match): each Scope grants (or, when Deny=true,
//     subtracts) its listed verbs on channel names that match its
//     Pattern.
//
// Evaluation order:
//
//  1. Walk the Scopes for any deny scope whose pattern matches and
//     whose verb list contains v — if found, return false immediately
//     ("deny wins", consistent with AWS IAM and similar systems).
//     Denies override both Chs entries and overlapping allow scopes.
//  2. Walk c.Chs for an exact-name match.
//  3. Walk the Scopes for an allow scope whose pattern matches and
//     whose verb list contains v.
//  4. Otherwise return false.
//
// channel is the raw wire-form channel name; it is parsed once and
// compared against any applicable Scope. A malformed channel name
// returns false (fail closed). An empty c.Chs and empty c.Scopes return
// false for every input.
//
// A Scope with Deny=true is visible in the token like any other claim —
// operators should not treat deny patterns as confidential channel
// metadata (see docs/src/channels/acl.md).
func (c Claims) Authorizes(channel string, v Verb) bool {
	// Pre-parse the channel name when any scope is present so both passes
	// can reuse the parsed form. A malformed name fails closed: it cannot
	// satisfy a deny (no harm), but it also cannot satisfy an allow.
	var name channels.Name
	var parsed bool
	if len(c.Scopes) > 0 {
		n, err := channels.ParseName(channel)
		if err == nil {
			name = n
			parsed = true
		}
	}
	// Pass 1: deny wins. Walk every deny scope FIRST. If any matches the
	// (channel, verb) pair we are about to authorize, reject — even if
	// Chs or an allow scope would otherwise have permitted it. A token
	// carrying a deny on a channel listed in Chs is still denied; this
	// is the spec ("deny ALWAYS wins") and is exercised in
	// scope_deny_test.go.
	if parsed {
		for i := range c.Scopes {
			if !c.Scopes[i].Deny {
				continue
			}
			if c.Scopes[i].MatchesVerb(name, v) {
				return false
			}
		}
	}
	// Pass 2: Chs exact-match. Verb-agnostic.
	for _, ch := range c.Chs {
		if ch == channel {
			return true
		}
	}
	if !parsed {
		return false
	}
	// Pass 3: allow scopes.
	for i := range c.Scopes {
		if c.Scopes[i].Deny {
			continue
		}
		// Index iteration so Scope.Compiled() mutates the slice element
		// rather than a copy — keeps the cache hot across calls.
		if c.Scopes[i].MatchesVerb(name, v) {
			return true
		}
	}
	return false
}

// Sentinel errors. Callers errors.Is against these instead of string-matching.
var (
	ErrMalformedToken   = errors.New("auth: malformed token")
	ErrInvalidSignature = errors.New("auth: signature does not verify")
	ErrUnsupportedAlg   = errors.New("auth: unsupported algorithm")
	ErrExpired          = errors.New("auth: token expired")
	ErrNotYetValid      = errors.New("auth: token not yet valid")
	ErrTypeMismatch     = errors.New("auth: token type mismatch")
	ErrChannelMismatch  = errors.New("auth: token does not authorize this channel")
)
