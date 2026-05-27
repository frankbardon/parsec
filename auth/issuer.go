package auth

import (
	"errors"
	"time"

	"github.com/frankbardon/parsec/ratelimit"
)

// Issuer mints Parsec JWTs with the right TTL bounds. It wraps a Signer
// with policy: refresh tokens cannot outlive their channel; access tokens
// cannot outlive their refresh token.
type Issuer struct {
	signer *Signer
	clock  func() time.Time

	// AccessTTL is the lifetime of access tokens minted for clients.
	// Default 5 minutes. Clamped to [1m, 1h].
	AccessTTL time.Duration
	// MaxRefreshTTL is the upper bound on refresh tokens. The actual
	// refresh TTL is min(channelTTL, MaxRefreshTTL). Default 1h.
	MaxRefreshTTL time.Duration
}

// NewIssuer constructs an Issuer over signer.
func NewIssuer(signer *Signer) *Issuer {
	return &Issuer{
		signer:        signer,
		clock:         time.Now,
		AccessTTL:     5 * time.Minute,
		MaxRefreshTTL: time.Hour,
	}
}

// PairResult is what IssuePair returns: both tokens and their absolute
// expirations.
type PairResult struct {
	AccessToken    string
	RefreshToken   string
	AccessExpires  time.Time
	RefreshExpires time.Time
}

// IssuePair mints an access + refresh token pair for sub on the named
// channel and stamps the provided scope grants into both tokens. The
// refresh token is bounded by min(channelTTL, MaxRefreshTTL); the
// access token by min(AccessTTL, refreshTTL).
//
// A nil/empty scopes slice produces tokens with no pattern grants —
// the channel listed in Chs is still authorized for every verb.
//
// Each scope's Pattern is validated against the channel grammar before
// signing; an invalid pattern yields PARSEC_INVALID_ARGUMENT.
func (i *Issuer) IssuePair(sub, channel string, channelTTL time.Duration, scopes []Scope) (PairResult, error) {
	if channel == "" {
		return PairResult{}, errors.New("auth: channel required")
	}
	if channelTTL <= 0 {
		return PairResult{}, errors.New("auth: channelTTL must be positive")
	}
	if err := validateScopes(scopes); err != nil {
		return PairResult{}, err
	}
	now := i.clock()
	refreshTTL := channelTTL
	if i.MaxRefreshTTL > 0 && refreshTTL > i.MaxRefreshTTL {
		refreshTTL = i.MaxRefreshTTL
	}
	accessTTL := i.AccessTTL
	if accessTTL > refreshTTL {
		accessTTL = refreshTTL
	}
	if accessTTL < time.Minute {
		accessTTL = time.Minute
	}
	accessExp := now.Add(accessTTL)
	refreshExp := now.Add(refreshTTL)
	scopesCopy := cloneScopesForClaim(scopes)
	access, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeAccess, Chs: []string{channel},
		Iat: now.Unix(), Exp: accessExp.Unix(),
		Scopes: scopesCopy,
	})
	if err != nil {
		return PairResult{}, err
	}
	refresh, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeRefresh, Chs: []string{channel},
		Iat: now.Unix(), Exp: refreshExp.Unix(),
		Scopes: scopesCopy,
	})
	if err != nil {
		return PairResult{}, err
	}
	return PairResult{
		AccessToken:    access,
		RefreshToken:   refresh,
		AccessExpires:  accessExp,
		RefreshExpires: refreshExp,
	}, nil
}

// IssueAccess mints a single access token from a verified refresh. The
// caller is responsible for verifying the refresh token first; this method
// trusts its inputs (sub, channel, refreshExp, scopes).
//
// The new access expiry is min(now+AccessTTL, refreshExp) so a refresh
// can never extend a session past the refresh token's own lifetime.
//
// A nil/empty scopes slice produces a token with no pattern grants —
// the channel listed in Chs is still authorized for every verb. Each
// scope's Pattern is validated against the channel grammar before
// signing.
func (i *Issuer) IssueAccess(sub, channel string, refreshExp time.Time, scopes []Scope) (string, time.Time, error) {
	if err := validateScopes(scopes); err != nil {
		return "", time.Time{}, err
	}
	now := i.clock()
	exp := now.Add(i.AccessTTL)
	if exp.After(refreshExp) {
		exp = refreshExp
	}
	if !exp.After(now) {
		return "", time.Time{}, errors.New("auth: refresh token has expired")
	}
	tok, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeAccess, Chs: []string{channel},
		Iat: now.Unix(), Exp: exp.Unix(),
		Scopes: cloneScopesForClaim(scopes),
	})
	return tok, exp, err
}

// IssueMgmt mints an operator token. ttl is clamped to [1h, 7d]; default 24h
// when zero.
func (i *Issuer) IssueMgmt(sub string, ttl time.Duration) (string, time.Time, error) {
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	if ttl < time.Hour {
		ttl = time.Hour
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}
	now := i.clock()
	exp := now.Add(ttl)
	tok, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeMgmt,
		Iat: now.Unix(), Exp: exp.Unix(),
	})
	return tok, exp, err
}

// SetClock overrides the time source. Used in tests.
func (i *Issuer) SetClock(c func() time.Time) { i.clock = c }

// validateScopes returns an error when any scope carries an unparseable
// pattern (notably the cross-visibility case rejected by ParsePattern).
// nil/empty slices are accepted as a no-op.
func validateScopes(scopes []Scope) error {
	for i := range scopes {
		if scopes[i].Pattern == "" {
			return errors.New("auth: scope pattern required")
		}
		if _, err := scopes[i].Compiled(); err != nil {
			return err
		}
		if len(scopes[i].Verbs) == 0 {
			return errors.New("auth: scope must list at least one verb")
		}
		for _, v := range scopes[i].Verbs {
			if !v.Valid() {
				return errors.New("auth: unknown verb in scope: " + string(v))
			}
		}
	}
	return nil
}

// cloneScopesForClaim copies the wire-relevant fields of each scope so
// the slice we sign carries no leaked matcher state (Compiled / mutex)
// to a JSON encoder. Returns nil for nil/empty input so the
// json:omitempty tag on Claims.Scopes elides the field. The Deny flag
// is preserved so negative scopes survive the round-trip.
func cloneScopesForClaim(scopes []Scope) []Scope {
	if len(scopes) == 0 {
		return nil
	}
	out := make([]Scope, len(scopes))
	for i := range scopes {
		out[i] = Scope{
			Pattern: scopes[i].Pattern,
			Verbs:   append([]Verb(nil), scopes[i].Verbs...),
			Deny:    scopes[i].Deny,
		}
	}
	return out
}

// IssuePairForChannels mints an access + refresh token pair that
// authorizes the listed channels (rather than a single channel like
// IssuePair). The refresh TTL is bounded by min(ttl, MaxRefreshTTL);
// the access TTL by min(AccessTTL, refresh). Used by the token
// broker for multi-channel issuance.
func (i *Issuer) IssuePairForChannels(sub string, chs []string, ttl time.Duration, scopes []Scope) (PairResult, error) {
	if len(chs) == 0 {
		return PairResult{}, errors.New("auth: at least one channel required")
	}
	if ttl <= 0 {
		return PairResult{}, errors.New("auth: ttl must be positive")
	}
	if err := validateScopes(scopes); err != nil {
		return PairResult{}, err
	}
	now := i.clock()
	refreshTTL := ttl
	if i.MaxRefreshTTL > 0 && refreshTTL > i.MaxRefreshTTL {
		refreshTTL = i.MaxRefreshTTL
	}
	accessTTL := i.AccessTTL
	if accessTTL > refreshTTL {
		accessTTL = refreshTTL
	}
	if accessTTL < time.Minute {
		accessTTL = time.Minute
	}
	accessExp := now.Add(accessTTL)
	refreshExp := now.Add(refreshTTL)
	chCopy := append([]string(nil), chs...)
	scopesCopy := cloneScopesForClaim(scopes)
	access, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeAccess, Chs: chCopy,
		Iat: now.Unix(), Exp: accessExp.Unix(),
		Scopes: scopesCopy,
	})
	if err != nil {
		return PairResult{}, err
	}
	refresh, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeRefresh, Chs: chCopy,
		Iat: now.Unix(), Exp: refreshExp.Unix(),
		Scopes: scopesCopy,
	})
	if err != nil {
		return PairResult{}, err
	}
	return PairResult{
		AccessToken:    access,
		RefreshToken:   refresh,
		AccessExpires:  accessExp,
		RefreshExpires: refreshExp,
	}, nil
}

// IssueAccessForChannels mints a fresh access token authorizing chs.
// The expiry is min(now+AccessTTL, refreshExp).
func (i *Issuer) IssueAccessForChannels(sub string, chs []string, refreshExp time.Time) (string, time.Time, error) {
	if len(chs) == 0 {
		return "", time.Time{}, errors.New("auth: at least one channel required")
	}
	now := i.clock()
	exp := now.Add(i.AccessTTL)
	if exp.After(refreshExp) {
		exp = refreshExp
	}
	if !exp.After(now) {
		return "", time.Time{}, errors.New("auth: refresh token has expired")
	}
	tok, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeAccess, Chs: append([]string(nil), chs...),
		Iat: now.Unix(), Exp: exp.Unix(),
	})
	return tok, exp, err
}

// IssuePairWithRateLimit is IssuePair with an additional per-token rate
// limit embedded in the access + refresh claims (rl). When the rate
// limiter is consulted at publish/subscribe time the override beats the
// server default for this subject.
//
// Passing a zero Limit (Rate == 0) is the same as IssuePair — no
// override claim is written.
func (i *Issuer) IssuePairWithRateLimit(sub, channel string, channelTTL time.Duration, override ratelimit.Limit) (PairResult, error) {
	if channel == "" {
		return PairResult{}, errors.New("auth: channel required")
	}
	if channelTTL <= 0 {
		return PairResult{}, errors.New("auth: channelTTL must be positive")
	}
	now := i.clock()
	refreshTTL := channelTTL
	if i.MaxRefreshTTL > 0 && refreshTTL > i.MaxRefreshTTL {
		refreshTTL = i.MaxRefreshTTL
	}
	accessTTL := i.AccessTTL
	if accessTTL > refreshTTL {
		accessTTL = refreshTTL
	}
	if accessTTL < time.Minute {
		accessTTL = time.Minute
	}
	accessExp := now.Add(accessTTL)
	refreshExp := now.Add(refreshTTL)
	var rl *ratelimit.Limit
	if !override.Unlimited() {
		n := override.Normalize()
		rl = &n
	}
	access, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeAccess, Chs: []string{channel},
		Iat: now.Unix(), Exp: accessExp.Unix(),
		RateLimitOverride: rl,
	})
	if err != nil {
		return PairResult{}, err
	}
	refresh, err := i.signer.Sign(Claims{
		Sub: sub, Typ: TypeRefresh, Chs: []string{channel},
		Iat: now.Unix(), Exp: refreshExp.Unix(),
		RateLimitOverride: rl,
	})
	if err != nil {
		return PairResult{}, err
	}
	return PairResult{
		AccessToken:    access,
		RefreshToken:   refresh,
		AccessExpires:  accessExp,
		RefreshExpires: refreshExp,
	}, nil
}
