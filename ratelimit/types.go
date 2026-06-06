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
	"sort"
	"time"

	"github.com/frankbardon/parsec/channels"
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
	// PerChannelPublish is the operator-configured override of Publish
	// for channels matching a pattern. Compiled by parsec.New; the
	// dispatcher picks the most-specific matching rule per call. Empty
	// = no overrides, every channel falls through to Publish.
	PerChannelPublish []ChannelRule `json:"per_channel_publish,omitempty"`
}

// ChannelRule binds a channel-name pattern to a Limit. Used by the
// publish gate to apply tighter (or looser) budgets on a per-channel
// basis: a hot channel like "public:metrics.heartbeat" can carry a
// higher ceiling than the default while a sensitive one like
// "private:admin.broadcast" can be locked down to a few events per
// minute.
type ChannelRule struct {
	// Pattern is the compiled channel-name matcher. See
	// channels.ParsePattern for the grammar.
	Pattern channels.Pattern `json:"-"`
	// Raw is the source string of Pattern, preserved so the manifest
	// and access log can identify which rule matched.
	Raw string `json:"pattern"`
	// Limit is the budget that applies when Pattern matches.
	Limit Limit `json:"limit"`
}

// CompileChannelRules parses raw[pattern]=limit map entries into
// compiled rules sorted most-specific first (more literal segments
// win; ties broken by raw string ascending). Invalid patterns or
// negative-rate limits return an error.
func CompileChannelRules(raw map[string]Limit) ([]ChannelRule, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]ChannelRule, 0, len(raw))
	for pat, lim := range raw {
		p, err := channels.ParsePattern(pat)
		if err != nil {
			return nil, err
		}
		out = append(out, ChannelRule{Pattern: p, Raw: pat, Limit: lim.Normalize()})
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := ruleSpecificity(out[i].Pattern), ruleSpecificity(out[j].Pattern)
		if si != sj {
			return si > sj
		}
		return out[i].Raw < out[j].Raw
	})
	return out, nil
}

// ruleSpecificity scores a Pattern: literal segments contribute most,
// single-stars less, trailing double-star least. Mirrors the schema
// registry's resolver so operators get consistent ranking semantics.
func ruleSpecificity(p channels.Pattern) int {
	// channels.Pattern exposes its raw string; count literal segments
	// by walking it. Cheap and avoids reaching into private fields.
	score := 0
	for _, seg := range patternSegments(p.String()) {
		switch seg {
		case "**":
			// no points
		case "*":
			score += 1
		default:
			score += 4
		}
	}
	return score
}

// patternSegments splits a pattern string into its parts the same way
// the channel grammar does (':' or '.' as separators).
func patternSegments(s string) []string {
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

// MatchPublish returns the Limit + matched rule's raw string for
// channel, or (Publish, "") when no per-channel rule matches.
func (rl RateLimits) MatchPublish(channel channels.Name) (Limit, string) {
	for _, rule := range rl.PerChannelPublish {
		if rule.Pattern.Matches(channel) {
			return rule.Limit, rule.Raw
		}
	}
	return rl.Publish, ""
}

// Normalize returns rl with every Limit normalized.
func (rl RateLimits) Normalize() RateLimits {
	out := RateLimits{
		Publish:           rl.Publish.Normalize(),
		Subscribe:         rl.Subscribe.Normalize(),
		TokenIssue:        rl.TokenIssue.Normalize(),
		PerChannelPublish: make([]ChannelRule, len(rl.PerChannelPublish)),
	}
	for i, r := range rl.PerChannelPublish {
		out.PerChannelPublish[i] = ChannelRule{
			Pattern: r.Pattern,
			Raw:     r.Raw,
			Limit:   r.Limit.Normalize(),
		}
	}
	return out
}

// Empty reports whether every limit is unlimited.
func (rl RateLimits) Empty() bool {
	if !rl.Publish.Unlimited() || !rl.Subscribe.Unlimited() || !rl.TokenIssue.Unlimited() {
		return false
	}
	for _, r := range rl.PerChannelPublish {
		if !r.Limit.Unlimited() {
			return false
		}
	}
	return true
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
	type perChannel struct {
		Pattern string `json:"pattern"`
		Limit   bucket `json:"limit"`
	}
	var pc []perChannel
	for _, r := range rl.PerChannelPublish {
		pc = append(pc, perChannel{Pattern: r.Raw, Limit: render(r.Limit)})
	}
	return json.Marshal(struct {
		Publish           bucket       `json:"publish"`
		Subscribe         bucket       `json:"subscribe"`
		TokenIssue        bucket       `json:"token_issue"`
		PerChannelPublish []perChannel `json:"per_channel_publish,omitempty"`
	}{
		Publish:           render(rl.Publish),
		Subscribe:         render(rl.Subscribe),
		TokenIssue:        render(rl.TokenIssue),
		PerChannelPublish: pc,
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
