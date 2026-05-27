package rpcclient

import (
	"github.com/twitchtv/twirp"

	perr "github.com/frankbardon/parsec/errors"
)

// mapErr translates a twirp.Error returned by the generated JSON client
// back into a parsec coded error so CLI consumers can dispatch on
// errors.Code(...). Non-twirp errors pass through unchanged.
//
// This is the inverse of service.toTwirpError; together they preserve the
// PARSEC_* → twirp.ErrorCode → PARSEC_* round-trip across the wire.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	te, ok := err.(twirp.Error)
	if !ok {
		return err
	}
	msg := te.Msg()
	switch te.Code() {
	case twirp.InvalidArgument:
		return perr.New(perr.InvalidArgument, msg)
	case twirp.NotFound:
		return perr.New(perr.ChannelNotFound, msg)
	case twirp.AlreadyExists:
		return perr.New(perr.ChannelExists, msg)
	case twirp.FailedPrecondition:
		return perr.New(perr.ChannelClosed, msg)
	case twirp.PermissionDenied:
		return perr.New(perr.AuthDenied, msg)
	case twirp.Unauthenticated:
		return perr.New(perr.AuthExpired, msg)
	case twirp.Unavailable:
		return perr.New(perr.BrokerNotReady, msg)
	case twirp.ResourceExhausted:
		return perr.New(perr.RateLimited, msg)
	default:
		// Preserve the twirp error for unmapped codes so callers can still
		// inspect the wire shape.
		return err
	}
}
