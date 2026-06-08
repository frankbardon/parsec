package rpcclient

import (
	"context"

	"github.com/frankbardon/parsec/rpc"
)

// RevokeToken marks a single token-id revoked on the server. The
// request is gated by the mgmt bearer the Client was constructed with.
func (c *Client) RevokeToken(ctx context.Context, tokenID, userID, reason string) error {
	_, err := c.rpc.RevokeToken(ctx, &rpc.RevokeTokenRequest{
		TokenId: tokenID,
		UserId:  userID,
		Reason:  reason,
	})
	return mapErr(err)
}

// RevokeUser invalidates every token previously issued to userID.
func (c *Client) RevokeUser(ctx context.Context, userID, reason string) error {
	_, err := c.rpc.RevokeUser(ctx, &rpc.RevokeUserRequest{
		UserId: userID,
		Reason: reason,
	})
	return mapErr(err)
}
