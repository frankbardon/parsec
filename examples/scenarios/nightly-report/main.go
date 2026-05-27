// Scenario 5 from the parsec brief:
//
//	"Nightly cron → email report." A scheduled job runs at midnight, mints
//	a per-date private channel, publishes the rolled-up report, and
//	expects nobody to be subscribed (the user is asleep). PublishOrSink
//	therefore falls back to email every time.
//
// Primitive demonstrated: building a date-scoped private channel name
// with channels.BuildName, then relying on PublishOrSink to always
// route through the email sink because presence is zero by design.
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
		slog.Error("nightly-report example failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	reg := sinks.NewRegistry()
	mailer, err := email.New(email.Config{
		Addr: "127.0.0.1:1", // closed; demo only
		From: "reports@parsec.example",
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

	// Channel ids must be lowercase ASCII + digits + - + _. Use yyyy-mm-dd.
	date := time.Now().UTC().Format("2006-01-02")
	name, err := channels.BuildName(channels.VisibilityPrivate, "cron", "report", date, "")
	if err != nil {
		return err
	}
	creds, err := p.CreatePrivate("ops-reports", name.String(), 1*time.Hour, nil)
	if err != nil {
		return err
	}
	logger.Info("nightly report channel minted",
		"date", date, "name", creds.Name.String())

	report, _ := json.Marshal(map[string]any{
		"date":         date,
		"ingested":     12_843,
		"errors":       17,
		"top_consumer": "user-42",
	})
	usedSink, err := p.PublishOrSink(ctx, name.String(), report,
		"email",
		email.Recipient{Address: "ops@parsec.example"},
		parsec.Message{
			Subject: "Nightly report — " + date,
			Body:    "12,843 events ingested; 17 errors; top consumer user-42.",
		})
	switch {
	case err != nil && usedSink:
		logger.Warn("email fallback attempted but failed (expected in demo)", "err", err)
	case err != nil:
		return err
	case usedSink:
		logger.Info("nightly report emailed (no subscriber, as expected)")
	default:
		logger.Info("nightly report delivered to live subscriber")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
	}
	return nil
}
