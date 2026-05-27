package auth

import (
	"sync"

	"github.com/frankbardon/parsec/channels"
)

// Verb is the typed action a Scope grants on a channel pattern. The wire
// values are the lowercase strings — they appear inside JWT payloads and
// the manifest, so changing them is a wire break.
type Verb string

const (
	// VerbSubscribe authorizes a subscribe over the websocket / SSE
	// surface. This is the verb the broker's subscribe authorizer
	// checks.
	VerbSubscribe Verb = "subscribe"
	// VerbPublish authorizes a client-side publish (server-side
	// publishes via the management RPC still go through the mgmt
	// bearer).
	VerbPublish Verb = "publish"
	// VerbManage authorizes per-channel management RPCs called with
	// an access token instead of a mgmt token — delegated administration.
	VerbManage Verb = "manage"
)

// AllVerbs lists every recognized verb in stable order. Used by the
// manifest so client SDKs can discover the surface.
var AllVerbs = []Verb{VerbSubscribe, VerbPublish, VerbManage}

// Valid reports whether v is one of the recognized verbs. Unknown verbs
// silently fail-closed at the authorize call — Scope.Authorizes will not
// honor them — but Valid is exposed for callers that want to validate at
// the boundary.
func (v Verb) Valid() bool {
	switch v {
	case VerbSubscribe, VerbPublish, VerbManage:
		return true
	default:
		return false
	}
}

// Scope is one entry in a Claims.Scopes set: a channel pattern plus the
// verbs the holder is granted on names that match.
//
// The Pattern field stays as raw text on the wire so the token shape
// matches the spec; Compiled() lazily parses and caches the structured
// Pattern for the matcher's hot path.
//
// A scope with Deny=true inverts the meaning: a matching (channel, verb)
// pair is REMOVED from the grant set with "deny wins" precedence. See
// Claims.Authorizes for the full evaluation order. The Deny field is
// omitted from the wire when false so allow-only tokens stay compact.
type Scope struct {
	Pattern string `json:"pat"`
	Verbs   []Verb `json:"v"`
	Deny    bool   `json:"deny,omitempty"`

	// compiled caches the parsed pattern. The mutex protects against
	// concurrent token-verification calls referencing the same Scope.
	compiled   channels.Pattern
	compiledOK bool
	compileErr error
	compileMu  sync.Mutex
}

// Compiled returns the parsed channels.Pattern, caching it on first call.
// Errors are sticky: a Scope with a malformed Pattern returns the same
// error every time without re-parsing.
func (s *Scope) Compiled() (channels.Pattern, error) {
	s.compileMu.Lock()
	defer s.compileMu.Unlock()
	if s.compiledOK || s.compileErr != nil {
		return s.compiled, s.compileErr
	}
	p, err := channels.ParsePattern(s.Pattern)
	if err != nil {
		s.compileErr = err
		return channels.Pattern{}, err
	}
	s.compiled = p
	s.compiledOK = true
	return p, nil
}

// Authorizes reports whether this scope grants verb v on the channel
// name. A malformed Pattern fails closed (returns false, never panics).
func (s *Scope) Authorizes(name channels.Name, v Verb) bool {
	p, err := s.Compiled()
	if err != nil {
		return false
	}
	if !p.Matches(name) {
		return false
	}
	for _, allowed := range s.Verbs {
		if allowed == v {
			return true
		}
	}
	return false
}

// AllowsVerb is a cheap check that only inspects the verb list, used by
// callers that have already matched the pattern.
func (s *Scope) AllowsVerb(v Verb) bool {
	for _, allowed := range s.Verbs {
		if allowed == v {
			return true
		}
	}
	return false
}

// IsDeny reports whether the scope is a negative grant. Convenience
// accessor so callers don't need to reach for the field directly when
// iterating a Claims.Scopes slice.
func (s *Scope) IsDeny() bool { return s.Deny }

// MatchesVerb is the shared "pattern matches AND verb is listed" check.
// Used by both the allow-pass and the deny-pass in Claims.Authorizes so
// the precedence logic stays in one place. Returns false on any
// compile / match / verb miss (fail closed).
func (s *Scope) MatchesVerb(name channels.Name, v Verb) bool {
	p, err := s.Compiled()
	if err != nil {
		return false
	}
	if !p.Matches(name) {
		return false
	}
	return s.AllowsVerb(v)
}
