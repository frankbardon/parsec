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
	return NewSubscribeAuthorizerWithLimiter(v, nil, ratelimit.Limit{})
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
	return func(ctx context.Context, userID string, ch channels.Name, event centrifuge.SubscribeEvent) error {
		if limiter != nil && !defaultLimit.Unlimited() {
			// userID is the centrifuge connection's authenticated subject.
			// When empty (anonymous transport) we have no stable per-client
			// identity available here — the SubscribeEvent does not
			// surface a remote address. In that case we allow without
			// charging the bucket; operators who want per-IP gating on
			// anonymous traffic should run behind a proxy that enforces
			// rate limits at L7.
			if userID != "" {
				key := ratelimit.BucketSubscribe + ":" + userID
				dec, err := limiter.Allow(ctx, key, 1)
				if err != nil {
					return errors.Wrap(errors.Internal, "rate limiter error", err)
				}
				if !dec.Allowed {
					return errors.New(errors.RateLimited, "subscribe rate limit exceeded")
				}
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
