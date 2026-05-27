package service

import (
	"context"
	"time"

	"github.com/frankbardon/parsec/rpc"
)

// ListKeys mirrors Service.ListKeys onto the RPC envelope.
func (a *RPCAdapter) ListKeys(ctx context.Context, _ *rpc.Empty) (*rpc.ListKeysResponse, error) {
	listing := a.S.ListKeys(ctx)
	out := &rpc.ListKeysResponse{ActiveKeyId: listing.ActiveKeyID}
	for _, k := range listing.Keys {
		out.Keys = append(out.Keys, &rpc.KeySummary{
			Id:        k.ID,
			Role:      k.Role,
			CreatedAt: k.CreatedAt,
			RetiredAt: k.RetiredAt,
		})
	}
	return out, nil
}

// GenerateKey mints a fresh key (verify-only).
func (a *RPCAdapter) GenerateKey(ctx context.Context, _ *rpc.Empty) (*rpc.KeySummary, error) {
	k, err := a.S.GenerateKey(ctx)
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.KeySummary{Id: k.ID, Role: k.Role, CreatedAt: k.CreatedAt, RetiredAt: k.RetiredAt}, nil
}

// PromoteKey transitions id to active.
func (a *RPCAdapter) PromoteKey(ctx context.Context, in *rpc.KeyRef) (*rpc.Empty, error) {
	if err := a.S.PromoteKey(ctx, in.GetId()); err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Empty{}, nil
}

// RetireKey marks id retired.
func (a *RPCAdapter) RetireKey(ctx context.Context, in *rpc.KeyRef) (*rpc.Empty, error) {
	if err := a.S.RetireKey(ctx, in.GetId()); err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Empty{}, nil
}

// ReloadKeys triggers a disk re-read.
func (a *RPCAdapter) ReloadKeys(ctx context.Context, _ *rpc.Empty) (*rpc.Empty, error) {
	if err := a.S.ReloadKeys(ctx); err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Empty{}, nil
}

// IssueMgmt mints a fresh mgmt bearer.
func (a *RPCAdapter) IssueMgmt(ctx context.Context, in *rpc.IssueMgmtRequest) (*rpc.IssueMgmtResponse, error) {
	res, err := a.S.IssueMgmt(ctx, in.GetSubject(), time.Duration(in.GetTtlSeconds())*time.Second)
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.IssueMgmtResponse{
		MgmtToken:     res.Token,
		ExpiresUnix:   res.Expires.Unix(),
		SignedByKeyId: res.SignedByKeyID,
	}, nil
}
