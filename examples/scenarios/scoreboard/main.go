// Scenario 7 from the parsec brief:
//
//	"Agent turn count → scoreboard increment." A public broadcast channel
//	receives a counter bump every time an AI user completes a turn. No
//	sink fallback — if nobody is listening, the tick is dropped. This is
//	the simplest possible parsec demo: open one public channel, publish
//	a few JSON ticks, exit clean.
//
// Primitive demonstrated: parsec.Publish on a public channel built with
// channels.BuildName.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/channels"
)

func main() {
	if err := run(); err != nil {
		slog.Error("scoreboard example failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	p, err := parsec.New(parsec.Options{Logger: logger})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	name, err := channels.BuildName(channels.VisibilityPublic, "scoreboard", "counter", "", "ai_users")
	if err != nil {
		return err
	}
	if _, err := p.OpenPublic(name.String(), 0); err != nil {
		return err
	}
	logger.Info("opened scoreboard channel", "name", name.String())

	// Simulate three agent turns landing in quick succession.
	for i := 1; i <= 3; i++ {
		payload, _ := json.Marshal(map[string]any{
			"tick": i,
			"by":   "agent-runner",
			"at":   time.Now().UTC().Format(time.RFC3339),
		})
		res, err := p.Publish(ctx, name.String(), payload)
		if err != nil {
			return err
		}
		logger.Info("tick published", "tick", i, "offset", res.Offset, "epoch", res.Epoch)
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	// Drain Run so we exit without a leaked goroutine. context.Canceled
	// is the expected return value here.
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
	}
	return nil
}
