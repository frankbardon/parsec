package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// luaSlidingWindow is the atomic sliding-window check used by RedisLimiter.
// Inputs (in order):
//
//	KEYS[1] = the sorted-set key                  (e.g. parsec:rl:publish:alice)
//	ARGV[1] = max_count (the ceiling — Burst)
//	ARGV[2] = window_ms (the rolling window)
//	ARGV[3] = now_ms    (server-supplied clock; avoids redis TIME drift)
//	ARGV[4] = n         (events being charged)
//	ARGV[5] = nonce     (uniqueness for ZADD members; the call-site supplies
//	                     a fresh value per Allow so concurrent calls don't
//	                     collide on the same now_ms/member tuple)
//
// Returns a 3-element table: {allowed, remaining, reset_ms}.
//
//   - allowed == 1 → n entries were added, remaining = max_count - count,
//     reset_ms = window_ms - (now_ms - oldest_score)
//   - allowed == 0 → no entries added, remaining = 0, reset_ms = time until
//     the oldest entry ages out.
//
// The EXPIRE keeps abandoned keys from accumulating in Redis.
const luaSlidingWindow = `
local max_count = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local now_ms   = tonumber(ARGV[3])
local n        = tonumber(ARGV[4])
local nonce    = ARGV[5]
local cutoff   = now_ms - window_ms

redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", cutoff)
local count = tonumber(redis.call("ZCARD", KEYS[1]))

if count + n <= max_count then
    for i = 1, n do
        -- member must be unique across concurrent calls at the same ms
        redis.call("ZADD", KEYS[1], now_ms, nonce .. ":" .. tostring(i))
    end
    redis.call("PEXPIRE", KEYS[1], window_ms + 10000)
    local oldest = redis.call("ZRANGE", KEYS[1], 0, 0, "WITHSCORES")
    local reset_ms = window_ms
    if #oldest >= 2 then
        local age = now_ms - tonumber(oldest[2])
        reset_ms = window_ms - age
        if reset_ms < 0 then reset_ms = 0 end
    end
    return {1, max_count - count - n, reset_ms}
end

-- Denied: surface the time until the oldest entry expires.
local oldest = redis.call("ZRANGE", KEYS[1], 0, 0, "WITHSCORES")
local reset_ms = window_ms
if #oldest >= 2 then
    local age = now_ms - tonumber(oldest[2])
    reset_ms = window_ms - age
    if reset_ms < 0 then reset_ms = 0 end
end
return {0, 0, reset_ms}
`

// RedisLimiter is the cross-node sliding-window backend. Multiple Parsec
// nodes that share a Redis instance share the same budget when they use
// the same KeyPrefix.
//
// The Lua script is loaded once (EVALSHA) and re-loaded on NOSCRIPT.
type RedisLimiter struct {
	client    redis.UniversalClient
	limit     Limit
	keyPrefix string

	scriptOnce sync.Once
	scriptSHA  string
	scriptErr  error
}

// NewRedisLimiter constructs a RedisLimiter backed by client. keyPrefix
// defaults to "parsec" when empty so it matches the rest of the Parsec
// Redis namespace.
func NewRedisLimiter(client redis.UniversalClient, limit Limit) *RedisLimiter {
	return &RedisLimiter{
		client:    client,
		limit:     limit.Normalize(),
		keyPrefix: "parsec",
	}
}

// WithKeyPrefix overrides the Redis key prefix.
func (r *RedisLimiter) WithKeyPrefix(p string) *RedisLimiter {
	if p != "" {
		r.keyPrefix = p
	}
	return r
}

// Limit returns the configured Limit.
func (r *RedisLimiter) Limit() Limit { return r.limit }

// Allow runs the sliding-window Lua script atomically on the Redis node
// holding KEYS[1]. The bucket label is encoded into the key so distinct
// surfaces (publish/subscribe/token-issue) cannot crowd each other.
//
// The key format is `<prefix>:rl:<bucket>:<subject>`. The bucket label is
// extracted from the leading prefix of key; if key contains a ":" the
// portion before the first ":" is the bucket. Callers that do not encode
// a bucket get "default".
func (r *RedisLimiter) Allow(ctx context.Context, key string, n int) (Decision, error) {
	return r.AllowWithLimit(ctx, key, n, r.limit)
}

// AllowWithLimit is Allow with a caller-supplied Limit. The same Redis
// keyspace is used regardless of lim; the Lua script reads max_count
// from ARGV on every call so per-token overrides work without
// reconstructing the limiter.
func (r *RedisLimiter) AllowWithLimit(ctx context.Context, key string, n int, lim Limit) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	lim = lim.Normalize()
	if lim.Unlimited() {
		return AllowDecisionUnlimited, nil
	}
	if n <= 0 {
		n = 1
	}
	bucket, subject := splitBucketKey(key)
	fullKey := fmt.Sprintf("%s:rl:%s:%s", r.keyPrefix, bucket, subject)
	now := time.Now().UnixMilli()
	windowMs := lim.Per.Milliseconds()
	if windowMs <= 0 {
		windowMs = int64(time.Second / time.Millisecond)
	}
	ceiling := lim.Burst
	if ceiling < lim.Rate {
		ceiling = lim.Rate
	}

	nonce, err := newNonce()
	if err != nil {
		return Decision{}, err
	}

	r.scriptOnce.Do(func() {
		sha, loadErr := r.client.ScriptLoad(ctx, luaSlidingWindow).Result()
		if loadErr != nil {
			r.scriptErr = loadErr
			return
		}
		r.scriptSHA = sha
	})

	args := []any{ceiling, windowMs, now, n, nonce}
	keys := []string{fullKey}

	var raw any
	if r.scriptSHA != "" {
		raw, err = r.client.EvalSha(ctx, r.scriptSHA, keys, args...).Result()
		if err != nil && isNoScript(err) {
			// Script was flushed on the Redis side — reload.
			sha, loadErr := r.client.ScriptLoad(ctx, luaSlidingWindow).Result()
			if loadErr != nil {
				return Decision{}, loadErr
			}
			r.scriptSHA = sha
			raw, err = r.client.EvalSha(ctx, r.scriptSHA, keys, args...).Result()
		}
	} else {
		// ScriptLoad on the once path failed; fall back to inline EVAL.
		raw, err = r.client.Eval(ctx, luaSlidingWindow, keys, args...).Result()
	}
	if err != nil {
		return Decision{}, err
	}
	return parseLuaResult(raw, windowMs)
}

// splitBucketKey separates "bucket:subject" into ("bucket","subject").
// When key has no ":" the bucket defaults to "default" so call sites
// passing bare subjects still produce a stable Redis key.
func splitBucketKey(key string) (bucket, subject string) {
	idx := strings.Index(key, ":")
	if idx < 0 {
		return "default", key
	}
	return key[:idx], key[idx+1:]
}

func newNonce() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func isNoScript(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "noscript")
}

// parseLuaResult unpacks the {allowed, remaining, reset_ms} table that
// the Lua script returns. The script always returns three integers but
// go-redis surfaces them as a []any of int64 values.
func parseLuaResult(raw any, windowMs int64) (Decision, error) {
	arr, ok := raw.([]any)
	if !ok || len(arr) != 3 {
		return Decision{}, errors.New("ratelimit: unexpected redis reply shape")
	}
	allowed, ok1 := arr[0].(int64)
	remaining, ok2 := arr[1].(int64)
	resetMs, ok3 := arr[2].(int64)
	if !ok1 || !ok2 || !ok3 {
		return Decision{}, errors.New("ratelimit: non-integer in redis reply")
	}
	reset := time.Duration(resetMs) * time.Millisecond
	if reset < 0 {
		reset = 0
	}
	if reset == 0 && allowed == 0 {
		reset = time.Duration(windowMs) * time.Millisecond
	}
	return Decision{
		Allowed:   allowed == 1,
		Remaining: int(remaining),
		Reset:     reset,
	}, nil
}
