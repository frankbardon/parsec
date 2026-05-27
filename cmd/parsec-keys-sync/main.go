// Package main is the parsec-keys-sync daemon: a tiny pub/sub bridge
// that mirrors Parsec keyring rotation events from one Redis (the
// source region) to N target Redis instances (the peer regions).
//
// Why this exists
//
// Each region's KeyRingStore
// publishes a version-number payload to <prefix>:keyring:events when
// its ring rotates. parsec nodes in *that* region see the event and
// reload, but peer regions never hear about it unless something carries
// the message across.
//
// parsec-keys-sync is that carrier. It does not replicate broker
// state, channel history, or anything else — it solely forwards key
// rotation events so a key retired in us-east is taken out of
// circulation in eu-west within a second.
//
// Deployment
//
// Run one process per *source* region. Pass --from for the source
// Redis URL and one --to per target. The daemon is stateless and
// idempotent (re-sending an already-applied event is a no-op). Restart
// it with the same flags after any crash — there's nothing to recover.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

// stringSliceFlag collects repeated --to values into a slice.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }

func (s *stringSliceFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("empty --to value")
	}
	*s = append(*s, v)
	return nil
}

func main() {
	var (
		from      = flag.String("from", "", "Source Redis URL (host:port or redis://...)")
		keyPrefix = flag.String("key-prefix", "parsec", "Namespace prefix for the keyring events channel")
		toFlag    stringSliceFlag
	)
	flag.Var(&toFlag, "to", "Target Redis URL. Repeat for multiple targets.")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if *from == "" {
		fail(logger, "--from is required")
	}
	if len(toFlag) == 0 {
		fail(logger, "--to is required (repeat for multiple targets)")
	}

	srcClient := redis.NewClient(&redis.Options{Addr: stripRedisScheme(*from)})
	defer srcClient.Close()
	targets := make([]*targetClient, 0, len(toFlag))
	for _, t := range toFlag {
		c := redis.NewClient(&redis.Options{Addr: stripRedisScheme(t)})
		targets = append(targets, &targetClient{URL: t, Client: c})
	}
	defer func() {
		for _, t := range targets {
			_ = t.Client.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGINT / SIGTERM cancel the context; the Run loop unwinds cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("parsec-keys-sync: received signal, shutting down", "signal", sig.String())
		cancel()
	}()

	logger.Info("parsec-keys-sync: starting",
		"from", *from,
		"to", []string(toFlag),
		"key_prefix", *keyPrefix,
	)
	if err := Run(ctx, logger, srcClient, targets, *keyPrefix); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("parsec-keys-sync: exiting on error", "err", err)
		os.Exit(1)
	}
	logger.Info("parsec-keys-sync: clean shutdown")
}

func fail(logger *slog.Logger, msg string) {
	logger.Error("parsec-keys-sync: " + msg)
	flag.Usage()
	os.Exit(2)
}

// targetClient pairs a Redis client with its declared URL so the
// forwarding log line carries the operator-visible identifier rather
// than the resolved host:port.
type targetClient struct {
	URL    string
	Client *redis.Client
}

// channelName returns the namespaced pub/sub channel Parsec's
// RedisKeyRingStore publishes rotation events on.
func channelName(prefix string) string { return prefix + ":keyring:events" }

// Run subscribes to the source's keyring events channel and republishes
// each payload to every target. Returns when ctx is canceled or the
// subscription fatally errors. Recoverable transport errors (connection
// drops) are retried with backoff.
func Run(ctx context.Context, logger *slog.Logger, src *redis.Client, targets []*targetClient, prefix string) error {
	ch := channelName(prefix)
	for {
		if err := runOnce(ctx, logger, src, targets, ch); err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return ctx.Err()
			}
			logger.Warn("parsec-keys-sync: subscription error, retrying", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}
		return nil
	}
}

// runOnce is one full subscribe + forward cycle. Returns when the
// subscription closes or ctx is canceled.
func runOnce(ctx context.Context, logger *slog.Logger, src *redis.Client, targets []*targetClient, ch string) error {
	pubsub := src.Subscribe(ctx, ch)
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		return fmt.Errorf("subscribe %s: %w", ch, err)
	}
	logger.Info("parsec-keys-sync: subscribed", "channel", ch)
	stream := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-stream:
			if !ok {
				return errors.New("pub/sub channel closed")
			}
			forward(ctx, logger, targets, ch, msg.Payload)
		}
	}
}

// forward publishes one payload to every target. Per-target failures
// log a warning but do not abort the loop — the next event from the
// source is independent.
func forward(ctx context.Context, logger *slog.Logger, targets []*targetClient, ch, payload string) {
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t *targetClient) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := t.Client.Publish(pctx, ch, payload).Err(); err != nil {
				logger.Warn("parsec-keys-sync: forward failed",
					"target", t.URL, "channel", ch, "err", err)
				return
			}
			logger.Info("parsec-keys-sync: forwarded",
				"target", t.URL, "channel", ch, "payload", payload)
		}(t)
	}
	wg.Wait()
}

// stripRedisScheme normalizes "redis://host:port" to "host:port" so the
// go-redis Options.Addr field is happy. Anything else is returned as-is.
func stripRedisScheme(s string) string {
	for _, p := range []string{"redis://", "rediss://"} {
		if strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}
