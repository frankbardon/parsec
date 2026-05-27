// Package email is a thin SMTP sink. It accepts a smtp.Addr-style config at
// construction time and delivers via net/smtp. No HTML formatting, no
// templating — that is the publisher's job.
package email

import (
	"context"
	"encoding/json"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/sinks"
)

// Config is the SMTP connection config. Username may be empty for relays
// without auth (e.g. local Postfix).
type Config struct {
	Addr     string // host:port, e.g. smtp.example.com:587
	From     string // RFC 5322 mailbox-list
	Username string
	Password string
}

// Recipient is the typed target for the email sink. Address must be a valid
// RFC 5322 mailbox.
type Recipient struct {
	Address string
}

// Sink implements sinks.Sink via net/smtp.SendMail.
type Sink struct {
	cfg  Config
	name string
}

// New constructs a Sink. Returns a coded error if Addr or From are empty.
func New(cfg Config) (*Sink, error) {
	if cfg.Addr == "" || cfg.From == "" {
		return nil, errors.New(errors.SinkConfig, "email sink requires Addr and From")
	}
	return &Sink{cfg: cfg, name: "email"}, nil
}

// NewNamed constructs a Sink with a custom name. Used by the factory so
// multiple email sinks (one per backend) can be registered.
func NewNamed(name string, cfg Config) (*Sink, error) {
	s, err := New(cfg)
	if err != nil {
		return nil, err
	}
	s.name = name
	return s, nil
}

func init() {
	sinks.RegisterFactory("email", func(name string, raw map[string]any) (sinks.Sink, error) {
		cfg := Config{
			Addr:     stringField(raw, "smtp_addr"),
			From:     stringField(raw, "from"),
			Username: stringField(raw, "auth_user"),
			Password: stringField(raw, "auth_pass"),
		}
		return NewNamed(name, cfg)
	})
}

func stringField(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// Name returns the registry key.
func (s *Sink) Name() string {
	if s.name == "" {
		return "email"
	}
	return s.name
}

// Send delivers m to the email Recipient.
func (s *Sink) Send(ctx context.Context, to sinks.Recipient, m sinks.Message) error {
	r, ok := to.(Recipient)
	if !ok {
		return errors.New(errors.InvalidArgument, "email sink expects sinks/email.Recipient")
	}
	if r.Address == "" {
		return errors.New(errors.InvalidArgument, "email recipient address is empty")
	}
	host := s.cfg.Addr
	if idx := strings.IndexByte(host, ':'); idx > 0 {
		host = host[:idx]
	}
	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, host)
	}
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		s.cfg.From, r.Address, m.Subject, m.Body)
	if err := smtp.SendMail(s.cfg.Addr, auth, s.cfg.From, []string{r.Address}, []byte(body)); err != nil {
		wrapped := errors.Wrap(errors.SinkUnavailable, "smtp send", err)
		// Classify: SMTP 4xx is transient (try again later); SMTP 5xx is
		// terminal; net errors / connect failures are transient.
		if classifySMTP(err) {
			return sinks.Transient(wrapped)
		}
		return sinks.Terminal(wrapped)
	}
	return nil
}

// classifySMTP returns true when err looks like a transient SMTP failure.
// We inspect the raw error string for an SMTP 4xx prefix; net-level
// failures fall back to the generic classifier in sinks/classify.go.
func classifySMTP(err error) bool {
	msg := err.Error()
	// SMTP 4xx codes are transient negative completion.
	for _, prefix := range []string{"421 ", "450 ", "451 ", "452 ", "454 "} {
		if strings.Contains(msg, prefix) {
			return true
		}
	}
	// Defer to the package-level classifier for net errors.
	return sinks.IsTransient(err)
}

// DecodeRecipient round-trips a persisted DLQ recipient back into the
// typed Recipient. Used by RedisDLQ.Replay.
func (s *Sink) DecodeRecipient(raw []byte) (sinks.Recipient, error) {
	var r Recipient
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return r, nil
}
