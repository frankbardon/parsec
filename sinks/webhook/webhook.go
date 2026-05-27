// Package webhook posts an arbitrary JSON envelope to a configured URL. Used
// when the consumer side is another HTTP service (an internal scoreboard, a
// downstream worker) rather than a person.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/sinks"
)

// isTransientHTTP returns true for HTTP statuses worth retrying.
func isTransientHTTP(status int) bool {
	if status == 429 {
		return true
	}
	return status >= 500 && status <= 599
}

// Config configures the outbound POST.
type Config struct {
	URL        string
	Headers    map[string]string
	HTTPClient *http.Client // optional
}

// Recipient lets the caller override the URL per-call. URL == "" means
// "use the config URL".
type Recipient struct {
	URL string
}

// Sink posts to a webhook endpoint.
type Sink struct {
	cfg    Config
	client *http.Client
	name   string
}

// New constructs a Sink. URL is required.
func New(cfg Config) (*Sink, error) {
	if cfg.URL == "" {
		return nil, errors.New(errors.SinkConfig, "webhook sink requires URL")
	}
	c := cfg.HTTPClient
	if c == nil {
		c = http.DefaultClient
	}
	return &Sink{cfg: cfg, client: c, name: "webhook"}, nil
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
	sinks.RegisterFactory("webhook", func(name string, raw map[string]any) (sinks.Sink, error) {
		url, _ := raw["url"].(string)
		headers := map[string]string{}
		if hraw, ok := raw["headers"].(map[string]any); ok {
			for k, v := range hraw {
				if s, ok := v.(string); ok {
					headers[k] = s
				}
			}
		}
		return NewNamed(name, Config{URL: url, Headers: headers})
	})
}

// Name returns the registry key.
func (s *Sink) Name() string {
	if s.name == "" {
		return "webhook"
	}
	return s.name
}

type webhookPayload struct {
	Subject  string            `json:"subject,omitempty"`
	Body     string            `json:"body,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Send posts m as JSON. If to is a Recipient with a non-empty URL, that URL
// overrides the configured one.
func (s *Sink) Send(ctx context.Context, to sinks.Recipient, m sinks.Message) error {
	url := s.cfg.URL
	if r, ok := to.(Recipient); ok && r.URL != "" {
		url = r.URL
	}
	body, err := json.Marshal(webhookPayload{Subject: m.Subject, Body: m.Body, Metadata: m.Metadata})
	if err != nil {
		return errors.Wrap(errors.Internal, "marshal webhook payload", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return errors.Wrap(errors.Internal, "webhook request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return sinks.Transient(errors.Wrap(errors.SinkUnavailable, "webhook post", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		wrapped := errors.New(errors.SinkUnavailable, "webhook returned "+resp.Status)
		if isTransientHTTP(resp.StatusCode) {
			return sinks.Transient(wrapped)
		}
		return sinks.Terminal(wrapped)
	}
	return nil
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
