package rpcclient

import (
	"context"
	"encoding/json"

	"github.com/frankbardon/parsec/rpc"
)

// DLQItemSummary is the CLI-shaped DLQ entry.
type DLQItemSummary struct {
	ID        string            `json:"id"`
	Sink      string            `json:"sink"`
	At        string            `json:"at"`
	Recipient json.RawMessage   `json:"recipient,omitempty"`
	Subject   string            `json:"subject,omitempty"`
	Body      string            `json:"body,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Attempts  int32             `json:"attempts"`
	LastError string            `json:"last_error,omitempty"`
}

// DLQListing is the CLI-shaped list result.
type DLQListing struct {
	Sink  string           `json:"sink,omitempty"`
	Items []DLQItemSummary `json:"items"`
}

// DLQCount is the CLI-shaped count result.
type DLQCount struct {
	Sink  string `json:"sink"`
	Count int32  `json:"count"`
}

// ListDLQ returns up to limit DLQ items for sink.
func (c *Client) ListDLQ(ctx context.Context, sink string, limit int) (DLQListing, error) {
	res, err := c.rpc.DlqList(ctx, &rpc.DlqListRequest{Sink: sink, Limit: int32(limit)})
	if err != nil {
		return DLQListing{}, mapErr(err)
	}
	out := DLQListing{Sink: sink, Items: make([]DLQItemSummary, 0, len(res.GetItems()))}
	for _, it := range res.GetItems() {
		out.Items = append(out.Items, DLQItemSummary{
			ID:        it.GetId(),
			Sink:      it.GetSink(),
			At:        it.GetAt(),
			Recipient: it.GetRecipient(),
			Subject:   it.GetSubject(),
			Body:      it.GetBody(),
			Metadata:  it.GetMetadata(),
			Attempts:  it.GetAttempts(),
			LastError: it.GetLastError(),
		})
	}
	return out, nil
}

// CountDLQ returns the size of the DLQ for sink.
func (c *Client) CountDLQ(ctx context.Context, sink string) (DLQCount, error) {
	res, err := c.rpc.DlqCount(ctx, &rpc.DlqCountRequest{Sink: sink})
	if err != nil {
		return DLQCount{}, mapErr(err)
	}
	return DLQCount{Sink: sink, Count: res.GetCount()}, nil
}

// DiscardDLQ removes id from the DLQ.
func (c *Client) DiscardDLQ(ctx context.Context, id string) error {
	_, err := c.rpc.DlqDiscard(ctx, &rpc.DlqDiscardRequest{Id: id})
	return mapErr(err)
}

// ReplayDLQ re-runs id through the original sink.
func (c *Client) ReplayDLQ(ctx context.Context, id string) error {
	_, err := c.rpc.DlqReplay(ctx, &rpc.DlqReplayRequest{Id: id})
	return mapErr(err)
}
