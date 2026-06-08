package parsec

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/channels"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/ratelimit"
)

// newSubscribePerChannelParsec boots a parsec with a generous global
// subscribe budget plus a tight per-channel rule on hot.** so isolated
// tests can verify the most-specific rule fires before the default
// budget is consumed.
func newSubscribePerChannelParsec(t *testing.T, perChannel map[string]ratelimit.Limit) (*Parsec, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{
		KeyRing:       ringFromSecret(t, secret),
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
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	time.Sleep(75 * time.Millisecond)
	return p, func() {
		cancel()
		<-done
	}
}

// TestCheckSubscribeLimit_TighterRuleFiresBeforeDefault confirms that a
// per-channel rule (1/s on hot.**) intercepts before the global default
// (50/s) is consumed: two back-to-back subscribes against the hot
// channel deplete the per-channel budget, while a cold-channel subscribe
// is unaffected.
func TestCheckSubscribeLimit_TighterRuleFiresBeforeDefault(t *testing.T) {
	p, stop := newSubscribePerChannelParsec(t, map[string]ratelimit.Limit{
		"private:webapp.hot.**": {Rate: 1, Per: time.Second},
	})
	defer stop()

	hot, _ := channels.ParseName("private:webapp.hot.42.feed")
	cold, _ := channels.ParseName("private:webapp.cold.42.feed")

	dec, err := p.CheckSubscribeLimit(context.Background(), "alice", hot, nil)
	if err != nil || !dec.Allowed {
		t.Fatalf("first hot subscribe: allowed=%v err=%v", dec.Allowed, err)
	}
	dec, err = p.CheckSubscribeLimit(context.Background(), "alice", hot, nil)
	if err != nil {
		t.Fatalf("second hot subscribe err=%v", err)
	}
	if dec.Allowed {
		t.Fatal("second hot subscribe must be denied by the per-channel rule")
	}

	// Cold channel keeps the global 50/s budget; 5 back-to-back subscribes
	// stay under it.
	for i := range 5 {
		dec, err = p.CheckSubscribeLimit(context.Background(), "alice", cold, nil)
		if err != nil || !dec.Allowed {
			t.Fatalf("cold subscribe %d: allowed=%v err=%v", i, dec.Allowed, err)
		}
	}
}

// TestCheckSubscribeLimit_KeysIsolatePerSubject confirms two subjects on
// the same rule do not share a budget — the per-channel key is
// namespaced with the subject suffix.
func TestCheckSubscribeLimit_KeysIsolatePerSubject(t *testing.T) {
	p, stop := newSubscribePerChannelParsec(t, map[string]ratelimit.Limit{
		"private:webapp.hot.**": {Rate: 1, Per: time.Second},
	})
	defer stop()

	hot, _ := channels.ParseName("private:webapp.hot.42.feed")
	for _, subj := range []string{"alice", "bob"} {
		dec, err := p.CheckSubscribeLimit(context.Background(), subj, hot, nil)
		if err != nil || !dec.Allowed {
			t.Fatalf("subject %s first: allowed=%v err=%v", subj, dec.Allowed, err)
		}
		dec, err = p.CheckSubscribeLimit(context.Background(), subj, hot, nil)
		if err != nil {
			t.Fatalf("subject %s second err=%v", subj, err)
		}
		if dec.Allowed {
			t.Fatalf("subject %s second subscribe should be denied", subj)
		}
	}
}

// TestCheckSubscribeLimit_MostSpecificRuleWins confirms tie-breaking
// when overlapping patterns are configured: more literal segments win
// over broader globs.
func TestCheckSubscribeLimit_MostSpecificRuleWins(t *testing.T) {
	p, stop := newSubscribePerChannelParsec(t, map[string]ratelimit.Limit{
		"private:webapp.hot.**":         {Rate: 100, Per: time.Second},
		"private:webapp.hot.42.feed":    {Rate: 1, Per: time.Second},
	})
	defer stop()

	hot, _ := channels.ParseName("private:webapp.hot.42.feed")
	dec, _ := p.CheckSubscribeLimit(context.Background(), "alice", hot, nil)
	if !dec.Allowed {
		t.Fatal("first subscribe under specific rule should allow")
	}
	dec, _ = p.CheckSubscribeLimit(context.Background(), "alice", hot, nil)
	if dec.Allowed {
		t.Fatal("specific rule should bite on second call")
	}
}

// TestSubscribeAuthorizer_PerChannelLimitFires drives the wired
// SubscribeAuthorizer (the broker's configured callback) end-to-end so
// the per-channel rule short-circuits before token verification.
func TestSubscribeAuthorizer_PerChannelLimitFires(t *testing.T) {
	p, stop := newSubscribePerChannelParsec(t, map[string]ratelimit.Limit{
		"private:webapp.hot.**": {Rate: 1, Per: time.Second},
	})
	defer stop()

	// Open the channel through the manager so the authorizer's IsOpen
	// guard passes.
	hot, _ := channels.ParseName("private:webapp.hot.42.feed")
	if _, err := p.Manager().CreatePrivate(hot, time.Minute); err != nil {
		t.Fatal(err)
	}

	// Mint an access token authorising the channel so token verification
	// would otherwise succeed; the rate limit must trip before that path.
	pair, err := p.Issuer().IssuePair("alice", hot.String(), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}

	authz := p.Broker().SubscribeAuthorizer()
	evt := centrifuge.SubscribeEvent{Token: pair.AccessToken}
	if err := authz(context.Background(), "alice", hot, evt); err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	err = authz(context.Background(), "alice", hot, evt)
	if err == nil {
		t.Fatal("second subscribe must be rate-limited")
	}
	pe, ok := err.(*perr.Error)
	if !ok {
		t.Fatalf("err type = %T, want *perr.Error", err)
	}
	if pe.Code != perr.RateLimited {
		t.Fatalf("Code = %s, want %s", pe.Code, perr.RateLimited)
	}
}

// TestPerChannelSubscribe_BadPatternAbortsNew confirms a malformed
// pattern aborts parsec.New with PARSEC_INVALID_ARGUMENT — operators
// catch misconfigurations at boot.
func TestPerChannelSubscribe_BadPatternAbortsNew(t *testing.T) {
	_, err := New(Options{
		Logger: slog.New(slog.DiscardHandler),
		PerChannelSubscribeLimits: map[string]ratelimit.Limit{
			"not a channel pattern": {Rate: 1, Per: time.Second},
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
