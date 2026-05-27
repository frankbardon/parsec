package service_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/ratelimit"
	"github.com/frankbardon/parsec/service"
)

// newRateLimitedService boots a parsec instance with a tiny publish-rate
// limit (2/s) and returns its Service + a cleanup. Tests then call
// svc.Publish with subject planted into ctx to exercise the gate.
func newRateLimitedService(t *testing.T) (*service.Service, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := auth.NewKeyRingFromSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsec.New(parsec.Options{
		KeyRing:       ring,
		SweepInterval: 50 * time.Millisecond,
		Logger:        slog.New(slog.DiscardHandler),
		RateLimits: ratelimit.RateLimits{
			Publish: ratelimit.Limit{Rate: 2, Per: time.Second},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var done atomic.Bool
	doneCh := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		done.Store(true)
		close(doneCh)
	}()
	time.Sleep(75 * time.Millisecond)
	svc := service.New(p, "test-0.0.0")
	return svc, func() {
		cancel()
		<-doneCh
	}
}

func TestService_PublishHitsRateLimit(t *testing.T) {
	svc, stop := newRateLimitedService(t)
	defer stop()
	// Open a channel so Publish can ship.
	const chName = "public:test.ratelimit.bucket"
	if _, err := svc.OpenPublicChannel(context.Background(), chName, time.Hour); err != nil {
		t.Fatal(err)
	}
	ctx := parsec.WithSubject(context.Background(), "alice")
	for i := 0; i < 2; i++ {
		if _, err := svc.Publish(ctx, chName, []byte("hi")); err != nil {
			t.Fatalf("iter=%d unexpected err: %v", i, err)
		}
	}
	_, err := svc.Publish(ctx, chName, []byte("hi"))
	if err == nil {
		t.Fatal("expected PARSEC_RATE_LIMITED on 3rd publish")
	}
	pe, ok := err.(*perr.Error)
	if !ok {
		t.Fatalf("err type = %T", err)
	}
	if pe.Code != perr.RateLimited {
		t.Fatalf("Code = %s, want %s", pe.Code, perr.RateLimited)
	}
}

func TestService_PublishUngatedWithoutIdentity(t *testing.T) {
	// Without subject + IP in context, no gating runs (tests / library
	// embedders that don't go through HTTP).
	svc, stop := newRateLimitedService(t)
	defer stop()
	const chName = "public:test.ratelimit.nogate"
	if _, err := svc.OpenPublicChannel(context.Background(), chName, time.Hour); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := svc.Publish(context.Background(), chName, []byte("hi")); err != nil {
			t.Fatalf("iter=%d unexpected err: %v", i, err)
		}
	}
}

func TestService_ManifestExposesRateLimits(t *testing.T) {
	svc, stop := newRateLimitedService(t)
	defer stop()
	env := svc.Manifest(context.Background())
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Payload struct {
			RateLimits map[string]struct {
				Rate  int    `json:"rate"`
				Per   string `json:"per"`
				Burst int    `json:"burst"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatal(err)
	}
	pub := probe.Payload.RateLimits["publish"]
	if pub.Rate != 2 || pub.Per != "1s" {
		t.Fatalf("publish rate limit not surfaced: %+v", pub)
	}
}

func TestService_PerTokenOverrideTightensBudget(t *testing.T) {
	// Build with publish=5/s default and an override of 1/s in the token.
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := auth.NewKeyRingFromSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsec.New(parsec.Options{
		KeyRing:       ring,
		SweepInterval: 50 * time.Millisecond,
		Logger:        slog.New(slog.DiscardHandler),
		RateLimits: ratelimit.RateLimits{
			Publish: ratelimit.Limit{Rate: 5, Per: time.Second},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() { _ = p.Run(ctx); close(doneCh) }()
	time.Sleep(75 * time.Millisecond)
	defer func() {
		cancel()
		<-doneCh
	}()
	svc := service.New(p, "test")
	const chName = "public:test.ratelimit.override"
	if _, err := svc.OpenPublicChannel(context.Background(), chName, time.Hour); err != nil {
		t.Fatal(err)
	}
	override := &ratelimit.Limit{Rate: 1, Per: time.Second}
	claims := &auth.Claims{Sub: "tight", Typ: auth.TypeMgmt, RateLimitOverride: override}
	rctx := parsec.WithSubject(context.Background(), "tight")
	rctx = parsec.WithClaims(rctx, claims)
	if _, err := svc.Publish(rctx, chName, []byte("hi")); err != nil {
		t.Fatalf("1st publish: %v", err)
	}
	_, err = svc.Publish(rctx, chName, []byte("hi"))
	if err == nil {
		t.Fatal("override of 1/s should deny 2nd publish")
	}
	pe, ok := err.(*perr.Error)
	if !ok || pe.Code != perr.RateLimited {
		t.Fatalf("want PARSEC_RATE_LIMITED, got %v", err)
	}
}
