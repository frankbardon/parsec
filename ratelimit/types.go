// Package ratelimit gates Parsec's publish/subscribe/refresh-token paths
// behind configurable per-key budgets.
//
// Two backends are shipped:
//
//   - MemoryLimiter — process-local sliding window. Used when Parsec runs
//     single-node (no Redis). Burst capacity is honored.
//   - RedisLimiter  — cross-node sliding window backed by Redis sorted sets
//     and a single-round-trip Lua script. Used when Options.RedisClient is
//     set so two parsec processes share the same budget.
//
// Both implement the Limiter interface. Decisions surface as
// (allowed, remaining, reset) so callers can stamp a Retry-After header.
package ratelimit

import (
	"context"
	"encoding/json"
	"time"
)

// Limiter is the per-key budget gate every gated surface calls.
//
// Allow returns Decision.Allowed=true when the caller may proceed and
// false when the key has exhausted its budget. n is the number of
// events being charged on this call (1 for every current callsite;
// reserved for future batch APIs). The Decision.Reset advises the
// caller how long to wait before the next attempt.
type Limiter interface {
	Allow(ctx context.Context, key string, n int) (Decision, error)
}

// Decision describes the outcome of a single Allow call.
type Decision struct {
	// Allowed reports whether the budget had room for n events.
	Allowed bool
	// Remaining is the budget left in the current window AFTER this call.
	// It is approximate for sliding-window limiters.
	Remaining int
	// Reset is the time until the oldest event in the window expires —
	// callers may surface this as the HTTP Retry-After header on 429.
	Reset time.Duration
}

// Limit describes a single budget: at most Rate events over the rolling
// window Per, with an instantaneous Burst peak. The zero Limit
// (Rate=0) means "unlimited" — the limiter returns Allowed=true without
// touching state.
type Limit struct {
	// Rate is the steady-state event budget over Per. Zero means unlimited.
	Rate int `json:"rate,omitempty"`
	// Per is the rolling window. Defaults to one second when Rate > 0.
	Per time.Duration `json:"per,omitempty"`
	// Burst is the instantaneous peak budget. Zero means Burst=Rate (no
	// peak above the steady-state). The effective ceiling at any instant
	// is max(Rate, Burst).
	Burst int `json:"burst,omitempty"`
}

// Normalize fills derived defaults so callers can rely on coherent values.
// Per defaults to one second when Rate > 0; Burst defaults to Rate.
// Negative values are clamped to zero.
func (l Limit) Normalize() Limit {
	if l.Rate < 0 {
		l.Rate = 0
	}
	if l.Burst < 0 {
		l.Burst = 0
	}
	if l.Per < 0 {
		l.Per = 0
	}
	if l.Rate > 0 && l.Per == 0 {
		l.Per = time.Second
	}
	if l.Burst == 0 {
		l.Burst = l.Rate
	}
	if l.Burst < l.Rate {
		l.Burst = l.Rate
	}
	return l
}

// Unlimited reports whether l disables rate limiting (Rate == 0).
func (l Limit) Unlimited() bool { return l.Normalize().Rate == 0 }

// RateLimits is the per-bucket policy bundle. Each Limit applies to a
// distinct "bucket" so a publish-heavy token does not eat into the
// subscribe budget.
type RateLimits struct {
	// Publish caps the number of publish RPCs per token subject.
	Publish Limit `json:"publish,omitempty"`
	// Subscribe caps the number of subscribe attempts per client identity
	// (user id or IP for anonymous traffic).
	Subscribe Limit `json:"subscribe,omitempty"`
	// TokenIssue caps RefreshToken RPC attempts per remote IP. Protects
	// the token endpoint from credential-stuffing.
	TokenIssue Limit `json:"token_issue,omitempty"`
}

// Normalize returns rl with every Limit normalized.
func (rl RateLimits) Normalize() RateLimits {
	return RateLimits{
		Publish:    rl.Publish.Normalize(),
		Subscribe:  rl.Subscribe.Normalize(),
		TokenIssue: rl.TokenIssue.Normalize(),
	}
}

// Empty reports whether every limit is unlimited.
func (rl RateLimits) Empty() bool {
	return rl.Publish.Unlimited() && rl.Subscribe.Unlimited() && rl.TokenIssue.Unlimited()
}

// MarshalJSON serializes RateLimits in an operator-friendly format —
// durations are surfaced as strings (e.g. "1s") so manifest output is
// readable.
func (rl RateLimits) MarshalJSON() ([]byte, error) {
	type bucket struct {
		Rate  int    `json:"rate"`
		Per   string `json:"per"`
		Burst int    `json:"burst"`
	}
	render := func(l Limit) bucket {
		l = l.Normalize()
		return bucket{Rate: l.Rate, Per: l.Per.String(), Burst: l.Burst}
	}
	return json.Marshal(struct {
		Publish    bucket `json:"publish"`
		Subscribe  bucket `json:"subscribe"`
		TokenIssue bucket `json:"token_issue"`
	}{
		Publish:    render(rl.Publish),
		Subscribe:  render(rl.Subscribe),
		TokenIssue: render(rl.TokenIssue),
	})
}

// Bucket names used in keys and metrics. Exported so call sites stay
// consistent.
const (
	BucketPublish    = "publish"
	BucketSubscribe  = "subscribe"
	BucketTokenIssue = "token-issue"
)

// AllowDecisionUnlimited is the canonical "unlimited" decision returned
// by both limiters when the configured Limit has Rate == 0.
var AllowDecisionUnlimited = Decision{Allowed: true, Remaining: -1, Reset: 0}
