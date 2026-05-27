package errors

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestError_FormatsCodeAndMsg(t *testing.T) {
	e := New(ChannelInvalid, "bad name")
	got := e.Error()
	if !strings.Contains(got, string(ChannelInvalid)) {
		t.Errorf("Error() missing code: %q", got)
	}
	if !strings.Contains(got, "bad name") {
		t.Errorf("Error() missing msg: %q", got)
	}
}

func TestError_FormatsCause(t *testing.T) {
	cause := errors.New("low-level boom")
	e := Wrap(Internal, "wrap it", cause)
	got := e.Error()
	if !strings.Contains(got, "low-level boom") {
		t.Errorf("Error() missing cause: %q", got)
	}
	if !strings.Contains(got, "wrap it") {
		t.Errorf("Error() missing msg: %q", got)
	}
	if !strings.Contains(got, string(Internal)) {
		t.Errorf("Error() missing code: %q", got)
	}
}

func TestError_Unwrap(t *testing.T) {
	cause := io.EOF
	e := Wrap(Internal, "x", cause)
	if got := errors.Unwrap(e); got != cause {
		t.Fatalf("Unwrap = %v, want %v", got, cause)
	}
	// Unwrap on an errorless error returns nil.
	e2 := New(Internal, "no cause")
	if got := errors.Unwrap(e2); got != nil {
		t.Fatalf("Unwrap of causeless = %v, want nil", got)
	}
}

func TestError_IsViaUnwrap(t *testing.T) {
	wrapped := Wrap(BrokerInternal, "publish", io.EOF)
	if !errors.Is(wrapped, io.EOF) {
		t.Fatal("errors.Is should walk Unwrap chain to io.EOF")
	}
}

func TestError_AsExposesCodedError(t *testing.T) {
	e := New(ChannelNotFound, "nope")
	// Wrap the coded error in fmt.Errorf to simulate cross-package wrapping.
	outer := fmt.Errorf("ctx: %w", e)
	var got *Error
	if !errors.As(outer, &got) {
		t.Fatal("errors.As failed to extract *Error from wrapped error")
	}
	if got.Code != ChannelNotFound {
		t.Fatalf("As code = %s, want %s", got.Code, ChannelNotFound)
	}
}

func TestNew_SetsFields(t *testing.T) {
	e := New(AuthDenied, "denied")
	if e.Code != AuthDenied {
		t.Errorf("Code = %s, want %s", e.Code, AuthDenied)
	}
	if e.Msg != "denied" {
		t.Errorf("Msg = %q, want %q", e.Msg, "denied")
	}
	if e.Cause != nil {
		t.Errorf("Cause should be nil, got %v", e.Cause)
	}
}

func TestWrap_PreservesCause(t *testing.T) {
	cause := errors.New("root")
	e := Wrap(AuthExpired, "expired", cause)
	if e.Cause != cause {
		t.Errorf("Cause = %v, want %v", e.Cause, cause)
	}
}

func TestCodeOf(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want Code
	}{
		{"nil-error", nil, ""},
		{"coded-error", New(ChannelExists, "x"), ChannelExists},
		{"plain-error", errors.New("boom"), Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CodeOf(c.in); got != c.want {
				t.Errorf("CodeOf(%v) = %s, want %s", c.in, got, c.want)
			}
		})
	}
}

func TestCodeConstants_AreUnique(t *testing.T) {
	all := []Code{
		ChannelInvalid, ChannelNotFound, ChannelExists, ChannelClosed, ChannelTTLExceeds,
		AuthDenied, AuthExpired,
		BrokerNotReady, BrokerInternal,
		SinkUnavailable, SinkConfig,
		InvalidArgument, Internal,
	}
	seen := make(map[Code]bool, len(all))
	for _, c := range all {
		if seen[c] {
			t.Errorf("duplicate code constant: %s", c)
		}
		seen[c] = true
		if !strings.HasPrefix(string(c), "PARSEC_") {
			t.Errorf("code %q should be PARSEC_-prefixed", c)
		}
	}
}
