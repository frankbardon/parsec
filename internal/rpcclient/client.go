// Package rpcclient is the CLI's adapter onto the generated Twirp JSON
// client. It re-shapes wire types into the CLI-friendly summary structs
// that get rendered into descriptor envelopes.
package rpcclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/frankbardon/parsec/rpc"
	"github.com/frankbardon/parsec/service"
)

// Client wraps the generated Twirp JSON client with CLI-friendly
// conversions.
type Client struct {
	rpc   rpc.ParsecService
	base  string
	token string
}

// New constructs a Client. token is the mgmt bearer; pass "" to leave it
// off (e.g. for the refresh-token RPC which authenticates by body).
func New(serverURL, token string) *Client {
	hc := &http.Client{
		Timeout:   30 * time.Second,
		Transport: bearerTransport(token, http.DefaultTransport),
	}
	return &Client{
		rpc:   rpc.NewParsecServiceJSONClient(serverURL, hc),
		base:  serverURL,
		token: token,
	}
}

// bearerTransport returns an http.RoundTripper that injects an
// Authorization: Bearer header on every request. If token is empty the
// inner transport is returned unchanged.
func bearerTransport(token string, inner http.RoundTripper) http.RoundTripper {
	if token == "" {
		return inner
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		// Clone so we never mutate the caller's request.
		clone := req.Clone(req.Context())
		clone.Header.Set("Authorization", "Bearer "+token)
		return inner.RoundTrip(clone)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ListChannels returns all managed channels.
func (c *Client) ListChannels(ctx context.Context) ([]ChannelSummary, error) {
	res, err := c.rpc.ListChannels(ctx, &rpc.Empty{})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]ChannelSummary, 0, len(res.GetChannels()))
	for _, ch := range res.GetChannels() {
		out = append(out, channelFromWire(ch))
	}
	return out, nil
}

// OpenPublic opens or re-opens a public channel.
func (c *Client) OpenPublic(ctx context.Context, name string, ttl time.Duration) (ChannelSummary, error) {
	res, err := c.rpc.OpenPublic(ctx, &rpc.OpenPublicRequest{Name: name, TtlSeconds: int64(ttl.Seconds())})
	if err != nil {
		return ChannelSummary{}, mapErr(err)
	}
	return channelFromWire(res.GetChannel()), nil
}

// ScopeArg is the wire-shape of one pattern grant on the client side.
// Mirrors auth.Scope without importing the auth package — the CLI/RPC
// client carries no business logic beyond shaping the request. Set Deny
// to mint a negative scope (subtracts from the grant set with
// deny-wins precedence).
type ScopeArg struct {
	Pattern string
	Verbs   []string
	Deny    bool
}

// CreatePrivate mints a private channel and returns the token pair.
// scopes attaches pattern-based grants to both tokens; pass nil for an
// exact-match-only grant on name. Each ScopeArg's Deny flag is
// forwarded so negative scopes survive the RPC boundary.
func (c *Client) CreatePrivate(ctx context.Context, subject, name string, ttl time.Duration, scopes []ScopeArg) (CredentialsSummary, error) {
	wire := make([]*rpc.Scope, 0, len(scopes))
	for _, s := range scopes {
		wire = append(wire, &rpc.Scope{
			Pattern: s.Pattern,
			Verbs:   append([]string(nil), s.Verbs...),
			Deny:    s.Deny,
		})
	}
	req := &rpc.CreatePrivateRequest{
		Subject:    subject,
		Name:       name,
		TtlSeconds: int64(ttl.Seconds()),
	}
	if len(wire) > 0 {
		req.Scopes = wire
	}
	res, err := c.rpc.CreatePrivate(ctx, req)
	if err != nil {
		return CredentialsSummary{}, mapErr(err)
	}
	return CredentialsSummary{
		Name:           res.GetName(),
		AccessToken:    res.GetAccessToken(),
		RefreshToken:   res.GetRefreshToken(),
		AccessExpires:  time.Unix(res.GetAccessExpiresUnix(), 0).UTC().Format(time.RFC3339),
		RefreshExpires: time.Unix(res.GetRefreshExpiresUnix(), 0).UTC().Format(time.RFC3339),
	}, nil
}

// RefreshToken exchanges a refresh token for a new access token.
func (c *Client) RefreshToken(ctx context.Context, refresh string) (RefreshSummary, error) {
	res, err := c.rpc.RefreshToken(ctx, &rpc.RefreshTokenRequest{RefreshToken: refresh})
	if err != nil {
		return RefreshSummary{}, mapErr(err)
	}
	return RefreshSummary{
		AccessToken:   res.GetAccessToken(),
		AccessExpires: time.Unix(res.GetAccessExpiresUnix(), 0).UTC().Format(time.RFC3339),
	}, nil
}

// GetChannel returns one channel snapshot.
func (c *Client) GetChannel(ctx context.Context, name string) (ChannelSummary, error) {
	res, err := c.rpc.GetChannel(ctx, &rpc.ChannelRef{Name: name})
	if err != nil {
		return ChannelSummary{}, mapErr(err)
	}
	return channelFromWire(res.GetChannel()), nil
}

// DeleteChannel removes a channel.
func (c *Client) DeleteChannel(ctx context.Context, name string) error {
	_, err := c.rpc.DeleteChannel(ctx, &rpc.ChannelRef{Name: name})
	return mapErr(err)
}

// Publish ships data to a channel.
func (c *Client) Publish(ctx context.Context, name string, data []byte) (service.PublishAck, error) {
	res, err := c.rpc.Publish(ctx, &rpc.PublishRequest{Name: name, Data: data})
	if err != nil {
		return service.PublishAck{}, mapErr(err)
	}
	return service.PublishAck{Offset: res.GetOffset(), Epoch: res.GetEpoch()}, nil
}

// SubscribeNDJSON streams a channel's publications via HTTP SSE for the
// CLI's `subscribe` probe.
func (c *Client) SubscribeNDJSON(ctx context.Context, name string, w io.Writer) error {
	url := fmt.Sprintf("%s/sse?channel=%s", c.base, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("subscribe: %s", resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// ChannelSummary is the CLI-shaped channel snapshot.
type ChannelSummary struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	TTL        string `json:"ttl"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
}

// CredentialsSummary is the CLI-shaped private credential pair.
type CredentialsSummary struct {
	Name           string `json:"name"`
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	AccessExpires  string `json:"access_expires"`
	RefreshExpires string `json:"refresh_expires"`
}

// RefreshSummary is the CLI-shaped refresh result.
type RefreshSummary struct {
	AccessToken   string `json:"access_token"`
	AccessExpires string `json:"access_expires"`
}

func channelFromWire(ch *rpc.ChannelSummary) ChannelSummary {
	if ch == nil {
		return ChannelSummary{}
	}
	return ChannelSummary{
		Name:       ch.GetName(),
		State:      ch.GetState(),
		TTL:        (time.Duration(ch.GetTtlSeconds()) * time.Second).String(),
		CreatedAt:  ch.GetCreatedAt(),
		LastActive: ch.GetLastActive(),
	}
}
