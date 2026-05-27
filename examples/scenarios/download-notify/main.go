// Scenario 1 from the parsec brief:
//
//	"Async download → notify-on-complete, email fallback." The user kicks
//	off a long-running export, walks away from the browser, and a worker
//	finishes the job several minutes later. If the session is still live
//	the JSON event lands in the realtime UI; otherwise an email is sent.
//
// Primitive demonstrated: parsec.PublishOrSink against a per-user
// private channel, with the email sink as the out-of-band fallback.
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
		slog.Error("download-notify example failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Email sink pointed at a deliberately unreachable SMTP endpoint so
	// the example demonstrates the fallback path without spamming.
	reg := sinks.NewRegistry()
	mailer, err := email.New(email.Config{
		Addr: "127.0.0.1:1", // closed port — Send will error
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

	name, err := channels.BuildName(channels.VisibilityPrivate, "webapp", "user", "42", "downloads")
	if err != nil {
		return err
	}
	creds, err := p.CreatePrivate("user-42", name.String(), 30*time.Minute, nil)
	if err != nil {
		return err
	}
	logger.Info("private channel ready",
		"name", creds.Name.String(),
		"access_expires", creds.AccessExpires.Format(time.RFC3339))

	payload, _ := json.Marshal(map[string]any{
		"status": "ready",
		"url":    "https://files.parsec.example/exports/42/report.csv",
	})
	usedSink, err := p.PublishOrSink(ctx, name.String(), payload,
		"email",
		email.Recipient{Address: "user-42@parsec.example"},
		parsec.Message{
			Subject: "Your download is ready",
			Body:    "Your CSV export finished. Follow the link to grab it.",
		})
	switch {
	case err != nil && usedSink:
		// Expected: the SMTP target is closed. Real deployments would
		// queue + retry; here we just log and continue.
		logger.Warn("email fallback attempted but failed (expected in demo)", "err", err)
	case err != nil:
		return err
	case usedSink:
		logger.Info("delivered via email sink (no live subscriber)")
	default:
		logger.Info("delivered to live websocket subscriber")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
	}
	return nil
}
