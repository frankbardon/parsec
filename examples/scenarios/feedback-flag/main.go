// Scenario 3 from the parsec brief:
//
//	"User flags AI answer → slack alert." A user clicks the thumbs-down
//	button on an AI response. The event is published on a public ops
//	channel that an internal dashboard may or may not be watching; in
//	parallel, parsec falls back to a Slack incoming webhook so the on-call
//	human sees the flag in their alerts channel.
//
// Primitive demonstrated: PublishOrSink against a public ops channel
// with the slack sink as the out-of-band fallback.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/channels"
	"github.com/frankbardon/parsec/sinks"
	"github.com/frankbardon/parsec/sinks/slack"
)

func main() {
	if err := run(); err != nil {
		slog.Error("feedback-flag example failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Stand up a stub HTTP server that pretends to be Slack's incoming
	// webhook endpoint. The example needs no network access — it talks
	// to itself. In production you would set WebhookURL to the real
	// hooks.slack.com URL.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("slack webhook stub received POST",
			"method", r.Method, "content_type", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer stub.Close()

	reg := sinks.NewRegistry()
	slk, err := slack.New(slack.Config{WebhookURL: stub.URL})
	if err != nil {
		return err
	}
	reg.Register(slk)

	p, err := parsec.New(parsec.Options{Sinks: reg, Logger: logger})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	name, err := channels.BuildName(channels.VisibilityPublic, "agent", "feedback", "", "flagged")
	if err != nil {
		return err
	}
	if _, err := p.OpenPublic(name.String(), 0); err != nil {
		return err
	}
	logger.Info("ops channel open", "name", name.String())

	payload, _ := json.Marshal(map[string]any{
		"event":      "thumbs_down",
		"answer_id":  "ans-c41",
		"user":       "user-42",
		"reason":     "hallucinated a citation",
		"flagged_at": time.Now().UTC().Format(time.RFC3339),
	})
	usedSink, err := p.PublishOrSink(ctx, name.String(), payload,
		"slack",
		slack.Recipient{}, // ignored — channel is fixed by webhook URL
		parsec.Message{
			Subject: "AI answer flagged",
			Body:    "ans-c41 was thumbs-downed by user-42",
		})
	if err != nil {
		return err
	}
	if usedSink {
		logger.Info("flag escalated to slack (no live ops subscriber)")
	} else {
		logger.Info("flag delivered to live ops dashboard")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
	}
	return nil
}
