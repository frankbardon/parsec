package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/frankbardon/parsec/sinks"
)

// DLQItemSummary is the surface-shape of a sinks.DLQItem. Recipient is
// rendered back as raw JSON so the wire and CLI views stay sink-agnostic.
type DLQItemSummary struct {
	ID        string            `json:"id"`
	Sink      string            `json:"sink"`
	At        string            `json:"at"`
	Recipient json.RawMessage   `json:"recipient,omitempty"`
	Subject   string            `json:"subject,omitempty"`
	Body      string            `json:"body,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Attempts  int               `json:"attempts"`
	LastError string            `json:"last_error,omitempty"`
}

// DLQListing is a list response.
type DLQListing struct {
	Items []DLQItemSummary `json:"items"`
}

// ListDLQ returns up to limit items for sink (empty sink = all).
func (s *Service) ListDLQ(ctx context.Context, sink string, limit int) (DLQListing, error) {
	dlq := s.p.DLQ()
	items, err := dlq.Items(ctx, sink, limit)
	if err != nil {
		return DLQListing{}, err
	}
	out := DLQListing{Items: make([]DLQItemSummary, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, dlqItemToSummary(it))
	}
	return out, nil
}

// CountDLQ returns the live DLQ count for sink.
func (s *Service) CountDLQ(ctx context.Context, sink string) (int, error) {
	return s.p.DLQ().Count(ctx, sink)
}

// DiscardDLQ removes id.
func (s *Service) DiscardDLQ(ctx context.Context, id string) error {
	return s.p.DLQ().Discard(ctx, id)
}

// ReplayDLQ re-runs id through the original sink.
func (s *Service) ReplayDLQ(ctx context.Context, id string) error {
	return s.p.DLQ().Replay(ctx, id)
}

// dlqItemToSummary normalises a stored item into the surface shape.
func dlqItemToSummary(it sinks.DLQItem) DLQItemSummary {
	out := DLQItemSummary{
		ID:        it.ID,
		Sink:      it.Sink,
		At:        it.At.UTC().Format(time.RFC3339Nano),
		Subject:   it.Message.Subject,
		Body:      it.Message.Body,
		Metadata:  it.Message.Metadata,
		Attempts:  it.Attempts,
		LastError: it.LastError,
	}
	switch v := it.Recipient.(type) {
	case nil:
		// no recipient
	case json.RawMessage:
		out.Recipient = v
	case []byte:
		out.Recipient = json.RawMessage(v)
	default:
		if raw, err := json.Marshal(v); err == nil {
			out.Recipient = raw
		}
	}
	return out
}
