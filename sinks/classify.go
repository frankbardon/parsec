// Classification helpers for the sink retry loop. The default IsTransient
// implementation recognises common network / protocol "blip" errors that are
// worth retrying with backoff. Sinks may also wrap an error in a value that
// implements the transientHinter interface to override the heuristic.
package sinks

import (
	"context"
	stderrors "errors"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"

	perr "github.com/frankbardon/parsec/errors"
)

// Sentinel context errors hoisted into package scope so the classifier can
// reference them without importing "context" at every call-site.
var (
	deadlineExceeded = context.DeadlineExceeded
	ctxCanceled      = context.Canceled
)

// transientHinter is the override hook. A sink (or middleware) can wrap a
// terminal error with `&transientErr{cause: err, transient: false}` and the
// classifier will respect that decision instead of guessing.
type transientHinter interface {
	Transient() bool
}

// transientErr is the canonical wrapper. Sinks should use Transient/Terminal
// helpers below rather than build this struct directly.
type transientErr struct {
	cause     error
	transient bool
}

func (e *transientErr) Error() string {
	if e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *transientErr) Unwrap() error   { return e.cause }
func (e *transientErr) Transient() bool { return e.transient }

// Transient marks err as worth retrying. Preserves wrapped *errors.Error
// codes so RPC mapping continues to work.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return &transientErr{cause: err, transient: true}
}

// Terminal marks err as not worth retrying.
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &transientErr{cause: err, transient: false}
}

// IsTransient returns true when err looks like a "try again" failure.
//
// Decision tree:
//
//  1. err == nil → false
//  2. context.Canceled / context.DeadlineExceeded → false (terminal, abort).
//  3. an unwrapped value that implements Transient() bool → use its answer.
//  4. *url.Error / net.OpError / net.Error.Timeout → true (network blip).
//  5. ECONNREFUSED / ECONNRESET / EPIPE → true.
//  6. *errors.Error with code SinkUnavailable and a recognisable HTTP/SMTP
//     transient prefix in the message → true.
//  7. anything else → false.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	// Context cancellation / deadline is terminal — give up immediately.
	if stderrors.Is(err, deadlineExceeded) || stderrors.Is(err, ctxCanceled) {
		return false
	}

	// Explicit hint from the sink wins over the heuristic.
	var th transientHinter
	if stderrors.As(err, &th) {
		return th.Transient()
	}

	// net.Error covers most std-library transport failures.
	var nerr net.Error
	if stderrors.As(err, &nerr) {
		if nerr.Timeout() {
			return true
		}
	}
	var opErr *net.OpError
	if stderrors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if stderrors.As(err, &urlErr) {
		return true
	}
	var syserr syscall.Errno
	if stderrors.As(err, &syserr) {
		switch syserr {
		case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EPIPE, syscall.ETIMEDOUT:
			return true
		}
	}

	// Coded errors from the reference sinks: inspect the message for HTTP
	// status codes or SMTP 4xx prefixes.
	var pe *perr.Error
	if stderrors.As(err, &pe) {
		if pe.Code == perr.SinkUnavailable && classifyHTTPSMTPMessage(pe.Msg) {
			return true
		}
		// Wrapped network cause underneath.
		if pe.Cause != nil {
			return IsTransient(pe.Cause)
		}
	}

	return false
}

// classifyHTTPSMTPMessage scans the (free-form) error string for known
// transient HTTP / SMTP markers. This is a heuristic — sinks that need
// precise control should wrap with Transient/Terminal explicitly.
func classifyHTTPSMTPMessage(msg string) bool {
	low := strings.ToLower(msg)
	// HTTP 5xx and 429.
	for _, frag := range []string{
		" 429 ", " 500 ", " 501 ", " 502 ", " 503 ", " 504 ", " 505 ",
		"internal server error", "bad gateway", "service unavailable",
		"gateway timeout", "too many requests",
	} {
		if strings.Contains(low, frag) {
			return true
		}
	}
	// SMTP 4xx (transient negative completion).
	for _, prefix := range []string{"4.", "421 ", "450 ", "451 ", "452 ", "454 "} {
		if strings.Contains(low, prefix) {
			return true
		}
	}
	return false
}

// Duration tagging shim used by classify.go callers to avoid an unused
// import warning when no other file in the package uses time.
var _ = time.Second
