package service

import (
	"context"

	"github.com/frankbardon/parsec/rpc"
)

// RevokeToken adapts the RPC envelope onto Service.RevokeToken.
func (a *RPCAdapter) RevokeToken(ctx context.Context, in *rpc.RevokeTokenRequest) (*rpc.Empty, error) {
	if err := a.S.RevokeToken(ctx, in.GetTokenId(), in.GetUserId(), in.GetReason()); err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Empty{}, nil
}

// RevokeUser adapts the RPC envelope onto Service.RevokeUser.
func (a *RPCAdapter) RevokeUser(ctx context.Context, in *rpc.RevokeUserRequest) (*rpc.Empty, error) {
	if err := a.S.RevokeUser(ctx, in.GetUserId(), in.GetReason()); err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Empty{}, nil
}
