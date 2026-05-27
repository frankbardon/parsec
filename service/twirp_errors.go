package service

import (
	stderrors "errors"

	"github.com/twitchtv/twirp"

	perr "github.com/frankbardon/parsec/errors"
)

// toTwirpError translates a coded parsec error into the twirp.Error code
// space so the generated server emits the correct HTTP status + envelope.
// Non-coded errors fall through as twirp.Internal.
//
// Mapping mirrors the hand-rolled writeServiceError in rpc/server.go that
// this layer replaces.
func toTwirpError(err error) error {
	if err == nil {
		return nil
	}
	// Already a twirp.Error (e.g. set by deeper layers). Pass through.
	if _, ok := err.(twirp.Error); ok {
		return err
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		// Unknown error → twirp internal.
		return twirp.InternalErrorWith(err)
	}
	switch pe.Code {
	case perr.ChannelInvalid, perr.InvalidArgument, perr.ChannelTTLExceeds:
		return twirp.NewError(twirp.InvalidArgument, pe.Msg)
	case perr.ChannelNotFound:
		return twirp.NewError(twirp.NotFound, pe.Msg)
	case perr.ChannelExists:
		return twirp.NewError(twirp.AlreadyExists, pe.Msg)
	case perr.ChannelClosed:
		return twirp.NewError(twirp.FailedPrecondition, pe.Msg)
	case perr.AuthDenied:
		return twirp.NewError(twirp.PermissionDenied, pe.Msg)
	case perr.AuthExpired:
		return twirp.NewError(twirp.Unauthenticated, pe.Msg)
	case perr.BrokerNotReady:
		return twirp.NewError(twirp.Unavailable, pe.Msg)
	case perr.SinkUnavailable, perr.SinkDLQOverflow:
		return twirp.NewError(twirp.Unavailable, pe.Msg)
	case perr.SinkDLQNotFound:
		return twirp.NewError(twirp.NotFound, pe.Msg)
	case perr.RateLimited:
		// Twirp's ResourceExhausted maps to HTTP 429 in the v8 spec.
		return twirp.NewError(twirp.ResourceExhausted, pe.Msg)
	default:
		return twirp.InternalErrorWith(pe)
	}
}
