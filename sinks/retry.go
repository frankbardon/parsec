package sinks

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"math"
	"time"

	perr "github.com/frankbardon/parsec/errors"
)

// RetryConfig governs the sink retry loop. Zero values are interpreted by
// Normalize.
type RetryConfig struct {
	// MaxAttempts is the total number of Send calls (initial + retries).
	// Default 5. A value of 1 disables retry.
	MaxAttempts int

	// BaseBackoff is the first wait after a failure. Default 1s.
	BaseBackoff time.Duration

	// MaxBackoff caps the exponential growth. Default 30s.
	MaxBackoff time.Duration

	// JitterFraction is the +/- proportion of jitter applied to each wait.
	// Default 0.2 (i.e. wait * uniform(0.8, 1.2)).
	JitterFraction float64
}

// Normalize fills in defaults for any zero / out-of-range field. The
// returned value is safe to pass to a Retrier.
func (c RetryConfig) Normalize() RetryConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.MaxBackoff < c.BaseBackoff {
		c.MaxBackoff = c.BaseBackoff
	}
	if c.JitterFraction < 0 {
		c.JitterFraction = 0
	}
	if c.JitterFraction > 1 {
		c.JitterFraction = 1
	}
	if c.JitterFraction == 0 {
		c.JitterFraction = 0.2
	}
	return c
}

// BackoffFor computes the wait before attempt (1-indexed). Attempt 1 returns
// 0 (no wait before the first try); attempt N returns base*2^(N-2) capped at
// MaxBackoff, with +/- JitterFraction multiplied in.
func (c RetryConfig) BackoffFor(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	c = c.Normalize()
	// exponent grows by attempt-2 because the first retry is at attempt=2.
	exp := attempt - 2
	if exp > 30 {
		exp = 30 // saturate before overflow
	}
	grown := float64(c.BaseBackoff) * math.Pow(2, float64(exp))
	if grown > float64(c.MaxBackoff) {
		grown = float64(c.MaxBackoff)
	}
	jitter := 1.0 + ((randFloat()*2 - 1) * c.JitterFraction)
	return time.Duration(grown * jitter)
}

// randFloat returns a uniform float in [0,1). Uses crypto/rand so the
// behaviour is identical in tests and production without seeding.
func randFloat() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	v := binary.LittleEndian.Uint64(b[:]) >> 11 // 53-bit mantissa
	return float64(v) / float64(1<<53)
}

// Retrier wraps a Sink with the retry-then-DLQ semantics described in the
// phase-12 spec. PublishOrSink stays synchronous: the caller blocks until
// the retry loop completes (success, DLQ push, or context cancellation).
//
// The Retrier is itself a Sink — the wrapped sink's Name is preserved so
// callers and the registry behave identically.
type Retrier struct {
	Inner Sink
	Cfg   RetryConfig
	DLQ   DLQ // optional; nil disables DLQ push
	// IsTransientFn overrides the default classifier. Optional.
	IsTransientFn func(error) bool
}

// NewRetrier wraps inner with the supplied config + DLQ.
func NewRetrier(inner Sink, cfg RetryConfig, dlq DLQ) *Retrier {
	return &Retrier{Inner: inner, Cfg: cfg.Normalize(), DLQ: dlq}
}

// Name returns the inner sink's name. Callers must not depend on whether a
// sink is wrapped.
func (r *Retrier) Name() string { return r.Inner.Name() }

// IsRetrier is a marker method used by the wrap-once gate in parsec.New to
// avoid double-wrapping a registered sink.
func (r *Retrier) IsRetrier() bool { return true }

// Unwrap exposes the wrapped sink so tests and tools can reach it without
// reflection.
func (r *Retrier) Unwrap() Sink { return r.Inner }

// Send drives the retry loop. On terminal failure it pushes a DLQItem and
// returns nil (the failure has been recorded). It returns an error only
// when the DLQ push itself fails OR when no DLQ is configured.
func (r *Retrier) Send(ctx context.Context, to Recipient, msg Message) error {
	cfg := r.Cfg.Normalize()
	classify := r.IsTransientFn
	if classify == nil {
		classify = IsTransient
	}
	var lastErr error
	attempt := 0
	for attempt = 1; attempt <= cfg.MaxAttempts; attempt++ {
		if attempt > 1 {
			wait := cfg.BackoffFor(attempt)
			if wait > 0 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return r.recordFailure(ctx, to, msg, attempt-1, ctx.Err(), cfg)
				}
			}
		}
		err := r.Inner.Send(ctx, to, msg)
		if err == nil {
			return nil
		}
		lastErr = err
		// Context cancellation always wins.
		if ctx.Err() != nil {
			return r.recordFailure(ctx, to, msg, attempt, ctx.Err(), cfg)
		}
		// Terminal errors abort the loop immediately.
		if !classify(err) {
			break
		}
	}
	return r.recordFailure(ctx, to, msg, attempt-1, lastErr, cfg)
}

// recordFailure pushes the final DLQItem when a DLQ is configured. When no
// DLQ is configured the original error bubbles up — callers can detect
// "retry exhausted" via the SinkUnavailable code on the wrapped error.
func (r *Retrier) recordFailure(ctx context.Context, to Recipient, msg Message, attempts int, lastErr error, _ RetryConfig) error {
	if r.DLQ == nil {
		if lastErr == nil {
			return perr.New(perr.SinkUnavailable, "sink retry exhausted with no recorded error")
		}
		return lastErr
	}
	item := DLQItem{
		Sink:      r.Inner.Name(),
		At:        time.Now().UTC(),
		Recipient: to,
		Message:   msg,
		Attempts:  attempts,
	}
	if lastErr != nil {
		item.LastError = lastErr.Error()
	}
	if err := r.DLQ.Push(ctx, item); err != nil {
		return perr.Wrap(perr.SinkDLQOverflow, "dlq push", err)
	}
	return nil
}

// WrapSink returns inner wrapped in a Retrier when cfg.MaxAttempts > 1 or a
// DLQ is configured. Already-wrapped sinks are returned unchanged so the
// helper is idempotent.
func WrapSink(inner Sink, cfg RetryConfig, dlq DLQ) Sink {
	if inner == nil {
		return nil
	}
	if r, ok := inner.(interface{ IsRetrier() bool }); ok && r.IsRetrier() {
		return inner
	}
	norm := cfg.Normalize()
	if norm.MaxAttempts <= 1 && dlq == nil {
		return inner
	}
	return NewRetrier(inner, norm, dlq)
}
