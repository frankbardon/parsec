package server_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/parsectest"
	"github.com/frankbardon/parsec/schema"
)

func TestSchemaBroadcastEndToEnd(t *testing.T) {
	reg := schema.NewMemoryRegistry()
	inst := parsectest.New(t, parsectest.WithOptions(func(o *parsec.Options) {
		o.SchemaHandler = schema.Handler(reg)
	}))

	if _, err := inst.OpenPublic(schema.ChangeChannel, time.Minute); err != nil {
		t.Fatalf("OpenPublic broadcast channel: %v", err)
	}

	pub := schema.NewPublisher(reg, func(ctx context.Context, ch string, data []byte) error {
		_, err := inst.Publish(ctx, ch, data)
		return err
	}, schema.PublisherOptions{})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = pub.Run(ctx) }()

	// Give Run's goroutine a moment to reach reg.Subscribe() before we
	// mutate the registry; otherwise the first Change races past an
	// empty subscriber map. 50ms is comfortably above scheduler jitter
	// and far below the 2s history-poll deadline below.
	time.Sleep(50 * time.Millisecond)

	pattern := schema.ChannelPattern{
		Pattern: "sessions:{id}",
		Version: 1,
		Aspects: map[string]schema.Aspect{
			"data": {Name: "data", PayloadSchema: &schema.JSONSchema{Type: "object"}},
		},
	}
	if err := reg.Register(pattern); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Poll broker history for our broadcast. parsec.broker keeps a
	// rolling window per channel, so a single Publish call lands as one
	// publication.
	var changes []schema.Change
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hist, err := inst.Broker().Node().History(schema.ChangeChannel,
			centrifuge.WithHistoryFilter(centrifuge.HistoryFilter{Limit: 50}))
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		changes = changes[:0]
		for _, p := range hist.Publications {
			var c schema.Change
			if err := json.Unmarshal(p.Data, &c); err != nil {
				t.Fatalf("unmarshal publication: %v", err)
			}
			changes = append(changes, c)
		}
		if len(changes) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(changes) != 1 {
		t.Fatalf("got %d broadcasts, want 1", len(changes))
	}
	if changes[0].Kind != schema.ChangeRegistered {
		t.Fatalf("kind = %q", changes[0].Kind)
	}
	if changes[0].Pattern != pattern.Pattern {
		t.Fatalf("pattern = %q", changes[0].Pattern)
	}
	if changes[0].Schema.Pattern != pattern.Pattern {
		t.Fatalf("embedded schema missing")
	}
}

