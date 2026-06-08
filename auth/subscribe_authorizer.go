package auth

import (
	"context"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/channels"
	"github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/ratelimit"
)

// NewSubscribeAuthorizer returns a broker subscribe authorizer that allows
// any well-formed public channel and verifies an access token for any
// private channel. The token must list the requested channel in its chs
// claim.
//
// The returned function matches broker.SubscribeAuthorizer.
func NewSubscribeAuthorizer(v *Verifier) func(ctx context.Context, userID string, ch channels.Name, event centrifuge.SubscribeEvent) error {
	return NewSubscribeAuthorizerWithGate(v, nil)
}

// NewSubscribeAuthorizerWithLimiter is NewSubscribeAuthorizer plus a
// per-key rate-limit gate that runs BEFORE token verification (so a
// stream of bad-token attempts cannot exhaust CPU on HMAC verifies).
//
// The key is the userID when set, otherwise the centrifuge client ID
// (which encodes the connection — best-effort proxy for IP when running
// behind the centrifuge transport).
//
// Passing limiter == nil or a zero Limit reverts to the no-rate-limit
// behaviour.
func NewSubscribeAuthorizerWithLimiter(v *Verifier, limiter ratelimit.Limiter, defaultLimit ratelimit.Limit) func(ctx context.Context, userID string, ch channels.Name, event centrifuge.SubscribeEvent) error {
	if limiter == nil || defaultLimit.Unlimited() {
		return NewSubscribeAuthorizerWithGate(v, nil)
	}
	gate := func(ctx context.Context, userID string, _ channels.Name) error {
		if userID == "" {
			return nil
		}
		key := ratelimit.BucketSubscribe + ":" + userID
		dec, err := limiter.Allow(ctx, key, 1)
		if err != nil {
			return errors.Wrap(errors.Internal, "rate limiter error", err)
		}
		if !dec.Allowed {
			return errors.New(errors.RateLimited, "subscribe rate limit exceeded")
		}
		return nil
	}
	return NewSubscribeAuthorizerWithGate(v, gate)
}

// SubscribeRateGate is the per-subscribe rate-limit callback. It runs
// BEFORE token verification with the authenticated userID + parsed
// channel; an empty userID means the connection is anonymous and
// operator-side L7 rate limiting is responsible for protection.
// Returning a coded *errors.Error denies the subscribe with that
// code; returning nil allows the authorizer to proceed.
type SubscribeRateGate func(ctx context.Context, userID string, ch channels.Name) error

// NewSubscribeAuthorizerWithGate is the generalized form: the rate-limit
// gate is supplied as a callback so the caller can resolve per-channel
// rules (or any other policy) instead of being constrained to a single
// flat per-subject Limit. Pass gate == nil to disable the gate.
func NewSubscribeAuthorizerWithGate(v *Verifier, gate SubscribeRateGate) func(ctx context.Context, userID string, ch channels.Name, event centrifuge.SubscribeEvent) error {
	return func(ctx context.Context, userID string, ch channels.Name, event centrifuge.SubscribeEvent) error {
		if gate != nil {
			if err := gate(ctx, userID, ch); err != nil {
				return err
			}
		}
		if !ch.IsPrivate() {
			return nil
		}
		if event.Token == "" {
			return errors.New(errors.AuthDenied, "private channel subscribe requires an access token")
		}
		claims, err := v.Verify(event.Token, TypeAccess)
		if err != nil {
			return mapVerifyError(err)
		}
		// Consult both Chs (exact match) and Scopes (pattern match) via
		// the unified Authorizes helper. Deny scopes are evaluated first
		// (deny-wins precedence).
		if !claims.Authorizes(ch.String(), VerbSubscribe) {
			return errors.New(errors.AuthDenied, "token does not authorize this channel")
		}
		return nil
	}
}

// mapVerifyError translates auth sentinels into PARSEC_AUTH_* codes the RPC
// layer can render.
func mapVerifyError(err error) error { return MapErr(err) }

// MapErr translates an auth sentinel into a parsec coded error. Exposed so
// the library facade and surface code can reuse the mapping.
func MapErr(err error) error {
	switch err {
	case ErrExpired:
		return errors.Wrap(errors.AuthExpired, "token expired", err)
	case ErrTypeMismatch, ErrChannelMismatch:
		return errors.Wrap(errors.AuthDenied, "token not authorized", err)
	default:
		return errors.Wrap(errors.AuthDenied, "token verification failed", err)
	}
}
