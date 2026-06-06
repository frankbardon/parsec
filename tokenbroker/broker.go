// Package tokenbroker is the policy point for issuing Parsec / Centrifugo
// connection tokens. A subscriber asks the broker for a token scoped to a
// list of channels; the broker consults a pluggable Authorizer to decide
// which channels the requesting principal is allowed to access, then
// mints a JWT that authorizes the granted subset.
//
// The broker is built on top of the auth.Issuer primitive — it does NOT
// reimplement signing. What it adds is:
//
//   - An HTTP surface (/parsec/token, /parsec/revoke,
//     /parsec/token/delegate) that user-facing clients hit before
//     opening their websocket.
//   - An Authorizer interface so each Parsec deployment can plug its
//     own ACL model (per-team, role-based, public-everything-private-
//     to-tenant, etc.).
//   - A revocation list so a mis-issued token can be invalidated before
//     its natural expiry.
//   - Delegated issuance so a backend service can mint a token "on
//     behalf of" a user, with both identities captured in the audit log.
//
// The broker depends on but does not own the schema registry — channel
// validity is the registry's concern, and channel-level ACL is the
// Authorizer's concern. Token signing belongs to auth.
package tokenbroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/frankbardon/parsec/auth"
)

// UserID is the opaque caller identity, supplied by the upstream
// authenticator (your user-auth system, OIDC IdP, internal session
// service, etc.). It is the input to the Authorizer.
type UserID string

// AuthDecision is the Authorizer's answer.
type AuthDecision struct {
	Granted []string         `json:"granted"`
	Denied  []DeniedChannel  `json:"denied,omitempty"`
}

// DeniedChannel pairs a rejected channel with a human-readable reason.
// The reason is surfaced in the API response so clients can show a
// useful error to the end user.
type DeniedChannel struct {
	Channel string `json:"channel"`
	Reason  string `json:"reason"`
}

// Authorizer decides which channels a user may access. The broker calls
// Authorize on each /parsec/token request.
type Authorizer interface {
	Authorize(ctx context.Context, user UserID, channels []string) AuthDecision
}

// AuthorizerFunc is the function form of Authorizer.
type AuthorizerFunc func(ctx context.Context, user UserID, channels []string) AuthDecision

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, user UserID, channels []string) AuthDecision {
	return f(ctx, user, channels)
}

// AllowAll is the trivial authorizer that grants every requested channel.
// Useful in dev / single-tenant deployments; production deployments
// should plug in their own.
var AllowAll Authorizer = AuthorizerFunc(func(_ context.Context, _ UserID, ch []string) AuthDecision {
	out := make([]string, len(ch))
	copy(out, ch)
	return AuthDecision{Granted: out}
})

// Authenticator resolves the caller's identity from a request bearer.
// The broker delegates user authentication entirely — Parsec deployments
// already have an auth system; the broker just consumes its tokens.
type Authenticator interface {
	Authenticate(ctx context.Context, bearer string) (UserID, error)
}

// AuthenticatorFunc is the function form of Authenticator.
type AuthenticatorFunc func(ctx context.Context, bearer string) (UserID, error)

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, bearer string) (UserID, error) {
	return f(ctx, bearer)
}

// ErrUnauthenticated is returned by Authenticator when the bearer
// cannot be resolved to a UserID.
var ErrUnauthenticated = errors.New("tokenbroker: unauthenticated")

// RevocationStore tracks revoked token IDs. The default is in-memory;
// production deployments should plug in Redis or a database.
type RevocationStore interface {
	Revoke(ctx context.Context, tokenID, userID, reason string) error
	IsRevoked(ctx context.Context, tokenID string) (bool, error)
	RevokeAllForUser(ctx context.Context, userID string) error
	IsUserRevoked(ctx context.Context, userID string, issuedAt time.Time) (bool, error)
}

// MemoryRevocations is the default in-memory revocation store.
//
// Entries are dropped when they age past MaxTTL — without that bound the
// map would grow forever even though every revoked token has long since
// expired. MaxTTL defaults to 24h (the mgmt-token clamp ceiling). Call
// StartPruner(interval) to run a background goroutine that purges aged
// entries, or call Prune(now) directly from a test harness.
type MemoryRevocations struct {
	mu       sync.RWMutex
	byToken  map[string]revocation
	byUserAt map[string]time.Time // user -> "revoke everything issued before this time"
	maxTTL   time.Duration
	clock    func() time.Time
}

type revocation struct {
	UserID string
	Reason string
	At     time.Time
}

// NewMemoryRevocations constructs an empty store with the default 24h
// MaxTTL.
func NewMemoryRevocations() *MemoryRevocations {
	return &MemoryRevocations{
		byToken:  map[string]revocation{},
		byUserAt: map[string]time.Time{},
		maxTTL:   24 * time.Hour,
		clock:    time.Now,
	}
}

// WithMaxTTL overrides the per-entry TTL. A zero or negative value
// resets to the 24h default.
func (m *MemoryRevocations) WithMaxTTL(d time.Duration) *MemoryRevocations {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d <= 0 {
		d = 24 * time.Hour
	}
	m.maxTTL = d
	return m
}

// SetClock overrides the time source. Used in tests.
func (m *MemoryRevocations) SetClock(c func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock = c
}

// Revoke marks tokenID as invalid.
func (m *MemoryRevocations) Revoke(_ context.Context, tokenID, userID, reason string) error {
	if tokenID == "" {
		return errors.New("tokenbroker: Revoke requires tokenID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byToken[tokenID] = revocation{UserID: userID, Reason: reason, At: m.clock().UTC()}
	return nil
}

// IsRevoked reports whether tokenID is in the revocation list. Entries
// older than MaxTTL are treated as expired and reported false.
func (m *MemoryRevocations) IsRevoked(_ context.Context, tokenID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.byToken[tokenID]
	if !ok {
		return false, nil
	}
	if m.clock().Sub(r.At) > m.maxTTL {
		return false, nil
	}
	return true, nil
}

// RevokeAllForUser invalidates every token issued to userID before now.
func (m *MemoryRevocations) RevokeAllForUser(_ context.Context, userID string) error {
	if userID == "" {
		return errors.New("tokenbroker: RevokeAllForUser requires userID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byUserAt[userID] = m.clock().UTC()
	return nil
}

// IsUserRevoked reports whether userID has a blanket revocation issued
// at-or-after issuedAt. Entries older than MaxTTL are treated as
// expired and reported false.
func (m *MemoryRevocations) IsUserRevoked(_ context.Context, userID string, issuedAt time.Time) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cutoff, ok := m.byUserAt[userID]
	if !ok {
		return false, nil
	}
	if m.clock().Sub(cutoff) > m.maxTTL {
		return false, nil
	}
	return !issuedAt.After(cutoff), nil
}

// Prune drops every entry older than MaxTTL relative to now. Safe to
// call from a test or any operator-owned goroutine.
func (m *MemoryRevocations) Prune(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.byToken {
		if now.Sub(r.At) > m.maxTTL {
			delete(m.byToken, id)
		}
	}
	for uid, at := range m.byUserAt {
		if now.Sub(at) > m.maxTTL {
			delete(m.byUserAt, uid)
		}
	}
}

// StartPruner runs Prune every interval until ctx is cancelled. A zero
// or negative interval disables the pruner (entries still expire on
// read; only the map shrinkage is skipped). Intended for long-lived
// processes; tests can call Prune directly.
func (m *MemoryRevocations) StartPruner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				m.Prune(now)
			}
		}
	}()
}

// IssueRequest is the inbound shape of POST /parsec/token.
type IssueRequest struct {
	Channels []string      `json:"channels"`
	TTL      time.Duration `json:"ttl,omitempty"`
}

// IssueResponse is what the broker sends back.
type IssueResponse struct {
	Token           string          `json:"token"`
	TokenID         string          `json:"token_id"`
	ExpiresAt       time.Time       `json:"expires_at"`
	ChannelsGranted []string        `json:"channels_granted"`
	ChannelsDenied  []DeniedChannel `json:"channels_denied,omitempty"`
}

// DelegateRequest is the body of POST /parsec/token/delegate.
type DelegateRequest struct {
	OnBehalfOf      string        `json:"on_behalf_of"`
	Channels        []string      `json:"channels"`
	LifetimeSeconds int           `json:"lifetime_seconds,omitempty"`
}

// RevokeRequest is the body of POST /parsec/revoke.
type RevokeRequest struct {
	TokenID string `json:"token_id,omitempty"`
	UserID  string `json:"user_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Options configures a Broker.
type Options struct {
	Issuer        *auth.Issuer
	Authorizer    Authorizer
	Authenticator Authenticator
	Revocations   RevocationStore
	// DefaultTTL is the access-token TTL when the caller did not
	// specify one. Zero falls back to auth.Issuer's own AccessTTL.
	DefaultTTL time.Duration
	// AuditLog, when non-nil, receives one entry per successful issuance.
	AuditLog AuditLogger
	// DelegateAuthorizer, when non-nil, gates /parsec/token/delegate.
	// The service identity is the bearer's UserID; the function returns
	// nil if the service is allowed to act on behalf of the target user.
	DelegateAuthorizer func(ctx context.Context, service UserID, onBehalfOf UserID) error
}

// AuditLogger receives one structured record per issued token. Pure
// observation — no return value affects issuance.
type AuditLogger interface {
	Audit(ctx context.Context, entry AuditEntry)
}

// AuditEntry captures the identity, channels, and lifetime of an issued
// token. Both the requester and the on-behalf-of identity are recorded
// for delegated issuance so audit trails capture both.
type AuditEntry struct {
	TokenID    string
	IssuedTo   UserID
	Delegator  UserID // service identity for delegated issuance; empty otherwise
	Channels   []string
	ExpiresAt  time.Time
	IssuedAt   time.Time
	RemoteAddr string
}

// AuditLoggerFunc is the function form of AuditLogger.
type AuditLoggerFunc func(ctx context.Context, entry AuditEntry)

// Audit implements AuditLogger.
func (f AuditLoggerFunc) Audit(ctx context.Context, e AuditEntry) { f(ctx, e) }

// Broker is the running token broker.
type Broker struct {
	opts Options
}

// New constructs a Broker. opts.Issuer, opts.Authorizer, and
// opts.Authenticator are required; opts.Revocations defaults to
// MemoryRevocations.
func New(opts Options) (*Broker, error) {
	if opts.Issuer == nil {
		return nil, errors.New("tokenbroker: Issuer required")
	}
	if opts.Authorizer == nil {
		return nil, errors.New("tokenbroker: Authorizer required")
	}
	if opts.Authenticator == nil {
		return nil, errors.New("tokenbroker: Authenticator required")
	}
	if opts.Revocations == nil {
		opts.Revocations = NewMemoryRevocations()
	}
	return &Broker{opts: opts}, nil
}

// Issue is the programmatic entry point — invoked by the HTTP handler
// for /parsec/token. The bearer is the requester's user-auth token;
// the broker resolves it to a UserID via the Authenticator.
func (b *Broker) Issue(ctx context.Context, bearer string, req IssueRequest) (IssueResponse, error) {
	uid, err := b.opts.Authenticator.Authenticate(ctx, bearer)
	if err != nil {
		return IssueResponse{}, err
	}
	return b.issueFor(ctx, uid, "", req)
}

// Delegate mints a token on behalf of req.OnBehalfOf. The bearer is the
// service's token. The DelegateAuthorizer (if configured) decides
// whether the service may act for the target user.
func (b *Broker) Delegate(ctx context.Context, bearer string, req DelegateRequest) (IssueResponse, error) {
	svc, err := b.opts.Authenticator.Authenticate(ctx, bearer)
	if err != nil {
		return IssueResponse{}, err
	}
	target := UserID(req.OnBehalfOf)
	if target == "" {
		return IssueResponse{}, errors.New("tokenbroker: on_behalf_of required")
	}
	if b.opts.DelegateAuthorizer != nil {
		if err := b.opts.DelegateAuthorizer(ctx, svc, target); err != nil {
			return IssueResponse{}, err
		}
	}
	ttl := time.Duration(req.LifetimeSeconds) * time.Second
	return b.issueFor(ctx, target, svc, IssueRequest{Channels: req.Channels, TTL: ttl})
}

// Revoke marks a token (or all of a user's tokens) invalid.
func (b *Broker) Revoke(ctx context.Context, bearer string, req RevokeRequest) error {
	if _, err := b.opts.Authenticator.Authenticate(ctx, bearer); err != nil {
		return err
	}
	if req.TokenID != "" {
		return b.opts.Revocations.Revoke(ctx, req.TokenID, req.UserID, req.Reason)
	}
	if req.UserID != "" {
		return b.opts.Revocations.RevokeAllForUser(ctx, req.UserID)
	}
	return errors.New("tokenbroker: token_id or user_id required")
}

// IsRevoked is the Centrifugo-side check hook: the connect/subscribe
// pipeline can consult this when a token presents itself. Token-level
// revocation is checked by ID; user-level revocation is checked against
// the issuedAt timestamp.
func (b *Broker) IsRevoked(ctx context.Context, tokenID, userID string, issuedAt time.Time) (bool, error) {
	if tokenID != "" {
		rev, err := b.opts.Revocations.IsRevoked(ctx, tokenID)
		if err != nil {
			return false, err
		}
		if rev {
			return true, nil
		}
	}
	if userID != "" {
		urev, err := b.opts.Revocations.IsUserRevoked(ctx, userID, issuedAt)
		if err != nil {
			return false, err
		}
		if urev {
			return true, nil
		}
	}
	return false, nil
}

func (b *Broker) issueFor(ctx context.Context, uid UserID, delegator UserID, req IssueRequest) (IssueResponse, error) {
	if len(req.Channels) == 0 {
		return IssueResponse{}, errors.New("tokenbroker: at least one channel required")
	}
	decision := b.opts.Authorizer.Authorize(ctx, uid, req.Channels)
	if len(decision.Granted) == 0 {
		return IssueResponse{
			ChannelsGranted: nil,
			ChannelsDenied:  decision.Denied,
		}, nil
	}
	ttl := req.TTL
	if ttl == 0 {
		ttl = b.opts.DefaultTTL
	}
	if ttl == 0 {
		ttl = time.Hour
	}
	// Pick the longest channel TTL — broker grants up to ttl, but the
	// underlying issuer clamps to min(channelTTL, MaxRefreshTTL). We
	// pass ttl as the channelTTL so the issuer treats it as the
	// requested ceiling.
	channel := decision.Granted[0]
	pair, err := b.opts.Issuer.IssuePair(string(uid), channel, ttl, nil)
	if err != nil {
		return IssueResponse{}, err
	}
	// Append additional granted channels to the access token's Chs by
	// reissuing — the auth.Issuer API only takes one channel in its
	// current form. We reuse the pair's metadata but reissue with the
	// full granted list via a second call path.
	if len(decision.Granted) > 1 {
		fullToken, exp, ierr := b.opts.Issuer.IssueAccessForChannels(string(uid), decision.Granted, pair.RefreshExpires)
		if ierr == nil {
			pair.AccessToken = fullToken
			pair.AccessExpires = exp
		}
	}
	tokenID := newTokenID()
	now := time.Now().UTC()
	if b.opts.AuditLog != nil {
		b.opts.AuditLog.Audit(ctx, AuditEntry{
			TokenID:   tokenID,
			IssuedTo:  uid,
			Delegator: delegator,
			Channels:  decision.Granted,
			ExpiresAt: pair.AccessExpires,
			IssuedAt:  now,
		})
	}
	return IssueResponse{
		Token:           pair.AccessToken,
		TokenID:         tokenID,
		ExpiresAt:       pair.AccessExpires,
		ChannelsGranted: decision.Granted,
		ChannelsDenied:  decision.Denied,
	}, nil
}

// newTokenID returns a 128-bit random ID rendered as hex.
func newTokenID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// RoleAuthorizer is a small built-in Authorizer that grants channels
// based on a role->pattern map. Each user has zero or more roles; each
// role has zero or more channel patterns that match the channels the
// role may access.
type RoleAuthorizer struct {
	UserRoles    func(ctx context.Context, user UserID) []string
	RolePatterns map[string][]string
}

// Authorize implements Authorizer. A channel is granted when at least
// one of the user's roles owns a pattern matching the channel.
func (r *RoleAuthorizer) Authorize(ctx context.Context, user UserID, channels []string) AuthDecision {
	roles := r.UserRoles(ctx, user)
	out := AuthDecision{}
	allowed := map[string]bool{}
	for _, role := range roles {
		for _, patternStr := range r.RolePatterns[role] {
			for _, ch := range channels {
				if matchSimple(patternStr, ch) {
					allowed[ch] = true
				}
			}
		}
	}
	for _, ch := range channels {
		if allowed[ch] {
			out.Granted = append(out.Granted, ch)
		} else {
			out.Denied = append(out.Denied, DeniedChannel{Channel: ch, Reason: "no matching role"})
		}
	}
	return out
}

// matchSimple is a tiny glob: '*' matches any single component (split on
// '.' or ':'). '**' matches the trailing remainder. Pure literal
// patterns must match exactly. This duplicates the schema package's
// pattern logic intentionally — tokenbroker does not depend on schema.
func matchSimple(pattern, name string) bool {
	if pattern == name {
		return true
	}
	pSegs := splitChannelSegs(pattern)
	nSegs := splitChannelSegs(name)
	for i, ps := range pSegs {
		if ps == "**" {
			return i <= len(nSegs)
		}
		if i >= len(nSegs) {
			return false
		}
		if ps == "*" {
			continue
		}
		if ps != nSegs[i] {
			return false
		}
	}
	return len(pSegs) == len(nSegs)
}

func splitChannelSegs(s string) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', ':':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
