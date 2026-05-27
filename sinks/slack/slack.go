// Package slack is a Slack incoming-webhook sink. One webhook URL per
// channel — the publisher picks the right sink instance for the channel it
// wants to post to.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/sinks"
)

// Config holds the per-channel webhook URL.
type Config struct {
	WebhookURL string
	HTTPClient *http.Client // optional; nil uses http.DefaultClient
}

// Recipient is unused but kept to satisfy the typed-recipient pattern; the
// channel is determined by the webhook URL itself. Callers may pass nil.
type Recipient struct{}

// Sink posts to the configured Slack webhook URL.
type Sink struct {
	cfg    Config
	client *http.Client
	name   string
}

// New constructs a Sink. Returns a coded error if WebhookURL is empty.
func New(cfg Config) (*Sink, error) {
	if cfg.WebhookURL == "" {
		return nil, errors.New(errors.SinkConfig, "slack sink requires WebhookURL")
	}
	c := cfg.HTTPClient
	if c == nil {
		c = http.DefaultClient
	}
	return &Sink{cfg: cfg, client: c, name: "slack"}, nil
}

// NewNamed constructs a Sink with a custom name. Used by the factory.
func NewNamed(name string, cfg Config) (*Sink, error) {
	s, err := New(cfg)
	if err != nil {
		return nil, err
	}
	s.name = name
	return s, nil
}

func init() {
	sinks.RegisterFactory("slack", func(name string, raw map[string]any) (sinks.Sink, error) {
		webhook, _ := raw["webhook_url"].(string)
		return NewNamed(name, Config{WebhookURL: webhook})
	})
}

// Name returns the registry key.
func (s *Sink) Name() string {
	if s.name == "" {
		return "slack"
	}
	return s.name
}

type slackPayload struct {
	Text     string            `json:"text"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Send posts the message to Slack. The Recipient is ignored; the channel is
// fixed by the webhook URL.
func (s *Sink) Send(ctx context.Context, _ sinks.Recipient, m sinks.Message) error {
	payload := slackPayload{Text: m.Body, Metadata: m.Metadata}
	if m.Subject != "" {
		payload.Text = "*" + m.Subject + "*\n" + m.Body
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(errors.Internal, "marshal slack payload", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.Wrap(errors.Internal, "slack request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return sinks.Transient(errors.Wrap(errors.SinkUnavailable, "slack post", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		wrapped := errors.New(errors.SinkUnavailable, "slack webhook returned "+resp.Status)
		if isTransientHTTP(resp.StatusCode) {
			return sinks.Transient(wrapped)
		}
		return sinks.Terminal(wrapped)
	}
	return nil
}

// isTransientHTTP returns true for HTTP status codes a Slack receiver may
// recover from: 5xx server failures + 429 rate limiting.
func isTransientHTTP(status int) bool {
	if status == 429 {
		return true
	}
	return status >= 500 && status <= 599
}

// DecodeRecipient is a no-op for slack — the channel is fixed by the
// webhook URL, so Recipient is always the zero value.
func (s *Sink) DecodeRecipient(_ []byte) (sinks.Recipient, error) {
	return Recipient{}, nil
}
