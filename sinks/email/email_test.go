package email

import (
	stderrors "errors"
	"testing"

	"github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/sinks"
)

func TestNew_RequiresAddrAndFrom(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"missing-addr", Config{From: "x@example.com"}},
		{"missing-from", Config{Addr: "smtp.example.com:587"}},
		{"both-missing", Config{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := New(c.cfg)
			if err == nil {
				t.Fatal("expected error")
			}
			if s != nil {
				t.Errorf("Sink should be nil on error, got %v", s)
			}
			var pe *errors.Error
			if !stderrors.As(err, &pe) {
				t.Fatalf("expected coded error, got %T (%v)", err, err)
			}
			if pe.Code != errors.SinkConfig {
				t.Fatalf("expected SinkConfig, got %s", pe.Code)
			}
		})
	}
}

func TestNew_Succeeds(t *testing.T) {
	s, err := New(Config{Addr: "smtp.example.com:587", From: "x@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "email" {
		t.Errorf("Name = %q, want email", s.Name())
	}
}

func TestSend_RequiresTypedRecipient(t *testing.T) {
	s, err := New(Config{Addr: "127.0.0.1:1", From: "x@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(t.Context(), "not-a-recipient", sinks.Message{Subject: "s"})
	if err == nil {
		t.Fatal("expected wrong-type error")
	}
	var pe *errors.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != errors.InvalidArgument {
		t.Fatalf("code = %s, want InvalidArgument", pe.Code)
	}
}

func TestSend_RequiresAddress(t *testing.T) {
	s, err := New(Config{Addr: "127.0.0.1:1", From: "x@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(t.Context(), Recipient{Address: ""}, sinks.Message{Subject: "s"})
	if err == nil {
		t.Fatal("expected empty-address error")
	}
	var pe *errors.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != errors.InvalidArgument {
		t.Fatalf("code = %s, want InvalidArgument", pe.Code)
	}
}

func TestSend_SMTPFailureReturnsSinkUnavailable(t *testing.T) {
	// 127.0.0.1:1 is virtually guaranteed to refuse connections (port 1).
	s, err := New(Config{Addr: "127.0.0.1:1", From: "x@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(t.Context(), Recipient{Address: "to@example.com"}, sinks.Message{Subject: "s", Body: "b"})
	if err == nil {
		t.Fatal("expected dial failure")
	}
	var pe *errors.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != errors.SinkUnavailable {
		t.Errorf("code = %s, want SinkUnavailable", pe.Code)
	}
}
