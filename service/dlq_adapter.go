package service

import (
	"context"

	"github.com/frankbardon/parsec/rpc"
)

// DlqList returns up to in.Limit DLQ items for the supplied sink (empty
// sink → all sinks).
func (a *RPCAdapter) DlqList(ctx context.Context, in *rpc.DlqListRequest) (*rpc.DlqListResponse, error) {
	listing, err := a.S.ListDLQ(ctx, in.GetSink(), int(in.GetLimit()))
	if err != nil {
		return nil, toTwirpError(err)
	}
	out := &rpc.DlqListResponse{Items: make([]*rpc.DlqItem, 0, len(listing.Items))}
	for _, it := range listing.Items {
		out.Items = append(out.Items, &rpc.DlqItem{
			Id:        it.ID,
			Sink:      it.Sink,
			At:        it.At,
			Recipient: it.Recipient,
			Subject:   it.Subject,
			Body:      it.Body,
			Metadata:  it.Metadata,
			Attempts:  int32(it.Attempts),
			LastError: it.LastError,
		})
	}
	return out, nil
}

// DlqCount returns the live size of the DLQ for sink.
func (a *RPCAdapter) DlqCount(ctx context.Context, in *rpc.DlqCountRequest) (*rpc.DlqCountResponse, error) {
	n, err := a.S.CountDLQ(ctx, in.GetSink())
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.DlqCountResponse{Count: int32(n)}, nil
}

// DlqDiscard removes a DLQ entry by id. Missing ids are silently OK.
func (a *RPCAdapter) DlqDiscard(ctx context.Context, in *rpc.DlqDiscardRequest) (*rpc.Empty, error) {
	if err := a.S.DiscardDLQ(ctx, in.GetId()); err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Empty{}, nil
}

// DlqReplay re-runs the stored item through the original sink. The DLQ
// entry remains in place — if replay fails again the Retrier pushes a new
// item, and the operator can compare attempt counts.
func (a *RPCAdapter) DlqReplay(ctx context.Context, in *rpc.DlqReplayRequest) (*rpc.Empty, error) {
	if err := a.S.ReplayDLQ(ctx, in.GetId()); err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Empty{}, nil
}
