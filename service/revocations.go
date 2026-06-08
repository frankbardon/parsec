package service

import (
	"context"

	perr "github.com/frankbardon/parsec/errors"
)

// RevokeToken marks a single access-token jti as revoked. Subsequent
// subscribe attempts presenting the token are denied by the subscribe
// authorizer wrapper. tokenID is required; userID and reason are
// recorded with the revocation for audit but do not gate the call.
//
// When parsec.Options.RevocationStore is nil the call returns
// PARSEC_INVALID_ARGUMENT so operators discover misconfiguration
// instead of silently no-op'ing.
func (s *Service) RevokeToken(ctx context.Context, tokenID, userID, reason string) error {
	if tokenID == "" {
		return perr.New(perr.InvalidArgument, "token_id required")
	}
	store := s.p.RevocationStore()
	if store == nil {
		return perr.New(perr.InvalidArgument, "revocation store not configured")
	}
	if err := store.Revoke(ctx, tokenID, userID, reason); err != nil {
		return perr.Wrap(perr.Internal, "revoke failed", err)
	}
	return nil
}

// RevokeUser invalidates every token previously issued to userID. The
// cutoff is the call's wall-clock; tokens minted after the call remain
// valid (use this when a user's credentials are compromised and they
// re-authenticate).
func (s *Service) RevokeUser(ctx context.Context, userID, reason string) error {
	if userID == "" {
		return perr.New(perr.InvalidArgument, "user_id required")
	}
	store := s.p.RevocationStore()
	if store == nil {
		return perr.New(perr.InvalidArgument, "revocation store not configured")
	}
	// The RevocationStore interface does not surface a reason on the
	// user-blanket path. Reason is accepted for symmetry with the
	// token-level call and audit-logged at the surface layer by the
	// access log; we drop it here.
	_ = reason
	if err := store.RevokeAllForUser(ctx, userID); err != nil {
		return perr.Wrap(perr.Internal, "revoke-user failed", err)
	}
	return nil
}
