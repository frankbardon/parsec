package rpcclient

import (
	"context"
	"time"

	"github.com/frankbardon/parsec/rpc"
)

// KeysListing is the CLI-shaped output of ListKeys.
type KeysListing struct {
	ActiveKeyID string       `json:"active_key_id"`
	Keys        []KeySummary `json:"keys"`
}

// KeySummary is the CLI-shaped ring key snapshot.
type KeySummary struct {
	ID        string `json:"id"`
	Alg       string `json:"alg,omitempty"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	RetiredAt string `json:"retired_at,omitempty"`
}

// MgmtTokenSummary is the CLI-shaped issued-mgmt-token result.
type MgmtTokenSummary struct {
	MgmtToken     string `json:"mgmt_token"`
	ExpiresUnix   int64  `json:"expires_unix"`
	SignedByKeyID string `json:"signed_by_key_id"`
}

// ListKeys returns every key in the ring.
func (c *Client) ListKeys(ctx context.Context) (KeysListing, error) {
	res, err := c.rpc.ListKeys(ctx, &rpc.Empty{})
	if err != nil {
		return KeysListing{}, mapErr(err)
	}
	out := KeysListing{ActiveKeyID: res.GetActiveKeyId()}
	for _, k := range res.GetKeys() {
		out.Keys = append(out.Keys, keyFromWire(k))
	}
	return out, nil
}

// GenerateKey mints a new key (verify-only). alg selects the
// algorithm; empty string defaults to HS256 server-side.
func (c *Client) GenerateKey(ctx context.Context, alg string) (KeySummary, error) {
	res, err := c.rpc.GenerateKey(ctx, &rpc.GenerateKeyRequest{Alg: alg})
	if err != nil {
		return KeySummary{}, mapErr(err)
	}
	return keyFromWire(res), nil
}

// PromoteKey makes id the active signing key.
func (c *Client) PromoteKey(ctx context.Context, id string) error {
	_, err := c.rpc.PromoteKey(ctx, &rpc.KeyRef{Id: id})
	return mapErr(err)
}

// RetireKey removes id from verification.
func (c *Client) RetireKey(ctx context.Context, id string) error {
	_, err := c.rpc.RetireKey(ctx, &rpc.KeyRef{Id: id})
	return mapErr(err)
}

// ReloadKeys forces the server to re-read the keyring file.
func (c *Client) ReloadKeys(ctx context.Context) error {
	_, err := c.rpc.ReloadKeys(ctx, &rpc.Empty{})
	return mapErr(err)
}

// IssueMgmt mints a fresh mgmt bearer signed by the active key.
func (c *Client) IssueMgmt(ctx context.Context, subject string, ttl time.Duration) (MgmtTokenSummary, error) {
	res, err := c.rpc.IssueMgmt(ctx, &rpc.IssueMgmtRequest{Subject: subject, TtlSeconds: int64(ttl.Seconds())})
	if err != nil {
		return MgmtTokenSummary{}, mapErr(err)
	}
	return MgmtTokenSummary{
		MgmtToken:     res.GetMgmtToken(),
		ExpiresUnix:   res.GetExpiresUnix(),
		SignedByKeyID: res.GetSignedByKeyId(),
	}, nil
}

func keyFromWire(k *rpc.KeySummary) KeySummary {
	if k == nil {
		return KeySummary{}
	}
	return KeySummary{
		ID:        k.GetId(),
		Alg:       k.GetAlg(),
		Role:      k.GetRole(),
		CreatedAt: k.GetCreatedAt(),
		RetiredAt: k.GetRetiredAt(),
	}
}
