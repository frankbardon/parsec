// Scenario 2 from the parsec brief:
//
//	"AI analysis → notify when complete, even after walk-away." A
//	long-running model invocation streams progress updates on a private
//	per-job channel. When the job finishes, the final event lands in the
//	live UI if it's still attached — otherwise it falls back to an email
//	so the user finds out next time they check.
//
// Primitive demonstrated: streaming intermediate publishes on a private
// channel and using PublishOrSink only for the terminal event.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/channels"
	"github.com/frankbardon/parsec/sinks"
	"github.com/frankbardon/parsec/sinks/email"
)

func main() {
	if err := run(); err != nil {
		slog.Error("agent-analysis example failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	reg := sinks.NewRegistry()
	mailer, err := email.New(email.Config{
		Addr: "127.0.0.1:1", // closed; demo only
		From: "no-reply@parsec.example",
	})
	if err != nil {
		return err
	}
	reg.Register(mailer)

	p, err := parsec.New(parsec.Options{Sinks: reg, Logger: logger})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	jobID := "job-7f3a"
	name, err := channels.BuildName(channels.VisibilityPrivate, "agent", "analysis", jobID, "progress")
	if err != nil {
		return err
	}
	creds, err := p.CreatePrivate("user-42", name.String(), 1*time.Hour, nil)
	if err != nil {
		return err
	}
	logger.Info("analysis channel minted", "job", jobID, "channel", creds.Name.String())

	// Intermediate progress publishes — drop into the channel; if nobody
	// is listening, we don't pester anyone. This is intentionally NOT
	// PublishOrSink: progress chatter shouldn't escalate to email.
	for _, pct := range []int{10, 35, 70, 95} {
		payload, _ := json.Marshal(map[string]any{
			"job":      jobID,
			"progress": pct,
		})
		if _, err := p.Publish(ctx, name.String(), payload); err != nil {
			return err
		}
		logger.Info("progress", "pct", pct)
		time.Sleep(50 * time.Millisecond)
	}

	// Terminal event: this one matters, so PublishOrSink it. If the user
	// walked away the email sink picks it up.
	final, _ := json.Marshal(map[string]any{
		"job":     jobID,
		"status":  "complete",
		"summary": "model produced 3 candidate insights",
	})
	usedSink, err := p.PublishOrSink(ctx, name.String(), final,
		"email",
		email.Recipient{Address: "user-42@parsec.example"},
		parsec.Message{
			Subject: "Analysis ready: " + jobID,
			Body:    "Your AI analysis is done. Open the dashboard to review.",
		})
	switch {
	case err != nil && usedSink:
		logger.Warn("email fallback attempted but failed (expected in demo)", "err", err)
	case err != nil:
		return err
	case usedSink:
		logger.Info("final event delivered via email sink")
	default:
		logger.Info("final event delivered to live subscriber")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
	}
	return nil
}
