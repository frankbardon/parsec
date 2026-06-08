package service_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/ratelimit"
	"github.com/frankbardon/parsec/service"
)

// newPerChannelService boots a parsec instance with a generous global
// publish budget (50/s) and a tight per-channel rule on
// "public:hot.**" (1/s). Hits to a non-matching channel run on the
// global budget; hits to the hot channel run on the tighter rule.
func newPerChannelService(t *testing.T, perChannel map[string]ratelimit.Limit) *service.Service {
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
			Publish: ratelimit.Limit{Rate: 50, Per: time.Second},
		},
		PerChannelPublishLimits: perChannel,
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
	t.Cleanup(func() {
		cancel()
		<-doneCh
	})
	return svc
}

func TestPerChannel_TighterRuleFiresBeforeDefault(t *testing.T) {
	svc := newPerChannelService(t, map[string]ratelimit.Limit{
		"public:hot.**": {Rate: 1, Per: time.Second},
	})

	const hot = "public:hot.metrics.feed"
	const cold = "public:cold.audit.feed"
	for _, ch := range []string{hot, cold} {
		if _, err := svc.OpenPublicChannel(context.Background(), ch, time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	ctx := parsec.WithSubject(context.Background(), "alice")

	if _, err := svc.Publish(ctx, hot, []byte("a")); err != nil {
		t.Fatalf("first hot publish: %v", err)
	}
	// Second hot publish must hit the per-channel ceiling immediately.
	_, err := svc.Publish(ctx, hot, []byte("b"))
	if !isRateLimited(err) {
		t.Fatalf("second hot publish err=%v, want PARSEC_RATE_LIMITED", err)
	}

	// Cold channel still has 50/s headroom — fire 5 publishes back-to-back.
	for i := 0; i < 5; i++ {
		if _, err := svc.Publish(ctx, cold, []byte("c")); err != nil {
			t.Fatalf("cold publish %d: %v", i, err)
		}
	}
}

func TestPerChannel_KeysIsolatePerSubject(t *testing.T) {
	// Two subjects on the same rule should not eat each other's budget.
	svc := newPerChannelService(t, map[string]ratelimit.Limit{
		"public:hot.**": {Rate: 1, Per: time.Second},
	})

	const hot = "public:hot.metrics.feed"
	if _, err := svc.OpenPublicChannel(context.Background(), hot, time.Hour); err != nil {
		t.Fatal(err)
	}

	for _, subj := range []string{"alice", "bob"} {
		ctx := parsec.WithSubject(context.Background(), subj)
		if _, err := svc.Publish(ctx, hot, []byte("ok")); err != nil {
			t.Fatalf("subject %s first publish: %v", subj, err)
		}
		if _, err := svc.Publish(ctx, hot, []byte("over")); !isRateLimited(err) {
			t.Fatalf("subject %s second publish err=%v, want PARSEC_RATE_LIMITED", subj, err)
		}
	}
}

func TestPerChannel_NonMatchingChannelUsesDefault(t *testing.T) {
	svc := newPerChannelService(t, map[string]ratelimit.Limit{
		"public:hot.**": {Rate: 1, Per: time.Second},
	})

	const other = "public:other.audit.feed"
	if _, err := svc.OpenPublicChannel(context.Background(), other, time.Hour); err != nil {
		t.Fatal(err)
	}
	ctx := parsec.WithSubject(context.Background(), "alice")
	for i := 0; i < 10; i++ {
		if _, err := svc.Publish(ctx, other, []byte("x")); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
}

func TestPerChannel_MostSpecificRuleWins(t *testing.T) {
	// Two overlapping rules; the more-literal pattern should beat the
	// broader one.
	svc := newPerChannelService(t, map[string]ratelimit.Limit{
		"public:hot.**":            {Rate: 100, Per: time.Second},
		"public:hot.metrics.feed":  {Rate: 1, Per: time.Second},
	})

	const hot = "public:hot.metrics.feed"
	if _, err := svc.OpenPublicChannel(context.Background(), hot, time.Hour); err != nil {
		t.Fatal(err)
	}
	ctx := parsec.WithSubject(context.Background(), "alice")
	if _, err := svc.Publish(ctx, hot, []byte("ok")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := svc.Publish(ctx, hot, []byte("over")); !isRateLimited(err) {
		t.Fatalf("expected specific rule to bite, got %v", err)
	}
}

func TestPerChannel_BadPatternAbortsNew(t *testing.T) {
	_, err := parsec.New(parsec.Options{
		Logger: slog.New(slog.DiscardHandler),
		PerChannelPublishLimits: map[string]ratelimit.Limit{
			"this is not a channel pattern": {Rate: 1, Per: time.Second},
		},
	})
	if err == nil {
		t.Fatal("expected parsec.New to reject bad pattern")
	}
	pe, ok := err.(*perr.Error)
	if !ok {
		t.Fatalf("err type = %T", err)
	}
	if pe.Code != perr.InvalidArgument {
		t.Fatalf("Code = %s, want %s", pe.Code, perr.InvalidArgument)
	}
}

func TestPerChannel_ManifestExposesRules(t *testing.T) {
	svc := newPerChannelService(t, map[string]ratelimit.Limit{
		"public:hot.**": {Rate: 5, Per: time.Second},
	})
	env := svc.Manifest(context.Background())
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.Contains(body, `"per_channel_publish"`) {
		t.Fatalf("manifest missing per_channel_publish: %s", body)
	}
	if !strings.Contains(body, `"public:hot.**"`) {
		t.Fatalf("manifest missing rule pattern: %s", body)
	}
}

func TestPerChannelSubscribe_ManifestExposesRules(t *testing.T) {
	svc := newPerChannelSubscribeService(t, map[string]ratelimit.Limit{
		"private:webapp.hot.**": {Rate: 5, Per: time.Second},
	})
	env := svc.Manifest(context.Background())
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.Contains(body, `"per_channel_subscribe"`) {
		t.Fatalf("manifest missing per_channel_subscribe: %s", body)
	}
	if !strings.Contains(body, `"private:webapp.hot.**"`) {
		t.Fatalf("manifest missing subscribe rule pattern: %s", body)
	}
}

// newPerChannelSubscribeService mirrors newPerChannelService but wires
// the rule against the subscribe bucket.
func newPerChannelSubscribeService(t *testing.T, perChannel map[string]ratelimit.Limit) *service.Service {
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
			Subscribe: ratelimit.Limit{Rate: 50, Per: time.Second},
		},
		PerChannelSubscribeLimits: perChannel,
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
	t.Cleanup(func() {
		cancel()
		<-doneCh
	})
	return svc
}

func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	pe, ok := err.(*perr.Error)
	if !ok {
		return false
	}
	return pe.Code == perr.RateLimited
}
