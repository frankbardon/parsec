// Package errors holds the typed Code system used across Parsec. Every coded
// error is convertible to a Twirp error and to a CLI exit reason.
package errors

import "fmt"

// Code is the typed error code. Values are DOMAIN_CATEGORY strings.
type Code string

const (
	// Channel codes.
	ChannelInvalid    Code = "PARSEC_CHANNEL_INVALID"
	ChannelNotFound   Code = "PARSEC_CHANNEL_NOT_FOUND"
	ChannelExists     Code = "PARSEC_CHANNEL_EXISTS"
	ChannelClosed     Code = "PARSEC_CHANNEL_CLOSED"
	ChannelTTLExceeds Code = "PARSEC_CHANNEL_TTL_EXCEEDS_MAX"

	// Auth codes.
	AuthDenied  Code = "PARSEC_AUTH_DENIED"
	AuthExpired Code = "PARSEC_AUTH_EXPIRED"

	// Broker codes.
	BrokerNotReady Code = "PARSEC_BROKER_NOT_READY"
	BrokerInternal Code = "PARSEC_BROKER_INTERNAL"

	// Sink codes.
	SinkUnavailable  Code = "PARSEC_SINK_UNAVAILABLE"
	SinkConfig       Code = "PARSEC_SINK_CONFIG_INVALID"
	SinkDLQOverflow  Code = "PARSEC_SINK_DLQ_OVERFLOW"
	SinkDLQNotFound  Code = "PARSEC_SINK_DLQ_NOT_FOUND"

	// Rate limit codes.
	RateLimited Code = "PARSEC_RATE_LIMITED"

	// Generic.
	InvalidArgument Code = "PARSEC_INVALID_ARGUMENT"
	Internal        Code = "PARSEC_INTERNAL"
)

// Error is a coded error. The Cause is preserved for log/debug but not
// exposed across the RPC boundary.
type Error struct {
	Code  Code
	Msg   string
	Cause error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Msg, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// New constructs a coded error with no cause.
func New(code Code, msg string) *Error {
	return &Error{Code: code, Msg: msg}
}

// Wrap constructs a coded error that wraps a lower-level cause.
func Wrap(code Code, msg string, cause error) *Error {
	return &Error{Code: code, Msg: msg, Cause: cause}
}

// CodeOf extracts the Code from err, or InternalCode if err is not a *Error.
// Useful at RPC boundaries.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return Internal
}
