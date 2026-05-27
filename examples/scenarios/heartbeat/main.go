// Scenario 4 from the parsec brief:
//
//	"Heartbeat job → status reports." A background service publishes a
//	periodic heartbeat on a public channel. Dashboards or anything else
//	that wants to know the service is alive subscribes. There is no sink
//	— if nobody is listening, the heartbeat is a tree falling in an empty
//	forest.
//
// Primitive demonstrated: a periodic-publisher loop driven by a ticker
// derived from the same cancellable context that drives parsec.Run.
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
		slog.Error("heartbeat example failed", "err", err)
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

	serviceID := "ingest-worker"
	name, err := channels.BuildName(channels.VisibilityPublic, "system", "heartbeat", serviceID, "")
	if err != nil {
		return err
	}
	if _, err := p.OpenPublic(name.String(), 0); err != nil {
		return err
	}
	logger.Info("heartbeat channel open", "name", name.String())

	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		t := time.NewTicker(400 * time.Millisecond)
		defer t.Stop()
		seq := 0
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				seq++
				payload, _ := json.Marshal(map[string]any{
					"service": serviceID,
					"seq":     seq,
					"status":  "ok",
					"at":      now.UTC().Format(time.RFC3339Nano),
				})
				if _, err := p.Publish(ctx, name.String(), payload); err != nil {
					// ctx cancellation surfaces here on shutdown; log
					// and bail so the goroutine doesn't spin.
					logger.Warn("heartbeat publish stopped", "err", err)
					return
				}
				logger.Info("heartbeat", "seq", seq)
			}
		}
	}()

	// Let the ticker emit ~5 beats, then cancel.
	time.Sleep(2200 * time.Millisecond)
	cancel()
	<-tickerDone

	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
	}
	return nil
}
