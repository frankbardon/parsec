package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/channels"
	"github.com/frankbardon/parsec/rpc"
)

// RPCAdapter implements rpc.ParsecService over a Service. Surface code wires
// this at boot — internal/server passes it to rpc.NewParsecServiceServer.
type RPCAdapter struct{ S *Service }

// NewRPCAdapter wraps svc.
func NewRPCAdapter(svc *Service) *RPCAdapter { return &RPCAdapter{S: svc} }

// Static assertion that *RPCAdapter implements the generated server
// interface. Catches signature drift at compile time.
var _ rpc.ParsecService = (*RPCAdapter)(nil)

// Manifest returns the manifest envelope as a JSONResponse.
func (a *RPCAdapter) Manifest(ctx context.Context, _ *rpc.Empty) (*rpc.JSONResponse, error) {
	env := a.S.Manifest(ctx)
	b, err := json.Marshal(env)
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.JSONResponse{Payload: b}, nil
}

// OpenPublic opens or re-opens a public channel.
func (a *RPCAdapter) OpenPublic(ctx context.Context, in *rpc.OpenPublicRequest) (*rpc.ChannelResponse, error) {
	ch, err := a.S.OpenPublicChannel(ctx, in.GetName(), time.Duration(in.GetTtlSeconds())*time.Second)
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.ChannelResponse{Channel: channelToWire(*ch)}, nil
}

// CreatePrivate mints a private channel and returns the token pair.
// Pattern scopes from the request are stamped into both tokens; an
// empty list produces an exact-match-only grant on the channel.
func (a *RPCAdapter) CreatePrivate(ctx context.Context, in *rpc.CreatePrivateRequest) (*rpc.Credentials, error) {
	scopes := scopesFromWire(in.GetScopes())
	creds, err := a.S.CreatePrivateChannel(ctx, in.GetSubject(), in.GetName(), time.Duration(in.GetTtlSeconds())*time.Second, scopes)
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Credentials{
		Name:               creds.Name.String(),
		AccessToken:        creds.AccessToken,
		RefreshToken:       creds.RefreshToken,
		AccessExpiresUnix:  creds.AccessExpires.Unix(),
		RefreshExpiresUnix: creds.RefreshExpires.Unix(),
	}, nil
}

// scopesFromWire converts the proto Scope shape to the auth.Scope shape.
// Returns nil for nil / empty input. The Deny flag is forwarded so
// negative scopes survive the RPC boundary.
func scopesFromWire(in []*rpc.Scope) []auth.Scope {
	if len(in) == 0 {
		return nil
	}
	out := make([]auth.Scope, 0, len(in))
	for _, s := range in {
		if s == nil {
			continue
		}
		verbs := make([]auth.Verb, 0, len(s.GetVerbs()))
		for _, v := range s.GetVerbs() {
			verbs = append(verbs, auth.Verb(v))
		}
		out = append(out, auth.Scope{
			Pattern: s.GetPattern(),
			Verbs:   verbs,
			Deny:    s.GetDeny(),
		})
	}
	return out
}

// RefreshToken exchanges a refresh token for a new access token.
func (a *RPCAdapter) RefreshToken(ctx context.Context, in *rpc.RefreshTokenRequest) (*rpc.RefreshTokenResponse, error) {
	res, err := a.S.RefreshAccess(ctx, in.GetRefreshToken())
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.RefreshTokenResponse{
		AccessToken:       res.AccessToken,
		AccessExpiresUnix: res.AccessExpires.Unix(),
	}, nil
}

// ListChannels returns every managed channel.
func (a *RPCAdapter) ListChannels(ctx context.Context, _ *rpc.Empty) (*rpc.ListChannelsResponse, error) {
	chs := a.S.ListChannels(ctx)
	out := make([]*rpc.ChannelSummary, 0, len(chs))
	for _, ch := range chs {
		out = append(out, channelToWire(ch))
	}
	return &rpc.ListChannelsResponse{Channels: out}, nil
}

// GetChannel returns one channel snapshot.
func (a *RPCAdapter) GetChannel(ctx context.Context, in *rpc.ChannelRef) (*rpc.ChannelResponse, error) {
	ch, err := a.S.GetChannel(ctx, in.GetName())
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.ChannelResponse{Channel: channelToWire(*ch)}, nil
}

// DeleteChannel removes a channel.
func (a *RPCAdapter) DeleteChannel(ctx context.Context, in *rpc.ChannelRef) (*rpc.Empty, error) {
	if err := a.S.DeleteChannel(ctx, in.GetName()); err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.Empty{}, nil
}

// Publish ships a message.
func (a *RPCAdapter) Publish(ctx context.Context, in *rpc.PublishRequest) (*rpc.PublishResponse, error) {
	ack, err := a.S.Publish(ctx, in.GetName(), in.GetData())
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.PublishResponse{Offset: ack.Offset, Epoch: ack.Epoch}, nil
}

// Presence returns the live subscriber count.
func (a *RPCAdapter) Presence(ctx context.Context, in *rpc.ChannelRef) (*rpc.PresenceResponse, error) {
	n, err := a.S.Presence(ctx, in.GetName())
	if err != nil {
		return nil, toTwirpError(err)
	}
	return &rpc.PresenceResponse{Subscribers: int32(n)}, nil
}

// channelToWire converts a channel snapshot to its proto wire shape.
func channelToWire(ch channels.Channel) *rpc.ChannelSummary {
	return &rpc.ChannelSummary{
		Name:       ch.Name.String(),
		State:      string(ch.State),
		TtlSeconds: int64(ch.TTL.Seconds()),
		CreatedAt:  ch.CreatedAt.UTC().Format(time.RFC3339Nano),
		LastActive: ch.LastActive.UTC().Format(time.RFC3339Nano),
	}
}
