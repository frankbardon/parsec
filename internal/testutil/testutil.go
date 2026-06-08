// Package testutil contains shared scaffolding for Parsec test packages:
// boot a real Parsec instance with an ephemeral key, wrap it in a Service,
// and (optionally) expose the composed HTTP surface via httptest.Server.
//
// Everything here uses real implementations — the broker, manager, signer,
// and verifier are all the production types. The only thing the helpers
// shortcut is the ceremony of secret generation and goroutine teardown.
package testutil

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/channels"
	"github.com/frankbardon/parsec/internal/server"
	"github.com/frankbardon/parsec/rpc"
	"github.com/frankbardon/parsec/service"
	"github.com/frankbardon/parsec/tokenbroker"
)

// Version is the version string stamped into the Service constructed by the
// helpers. Tests should not depend on the value; assert against this const.
const Version = "test-0.0.0"

// RingFromSecret wraps secret in a single-key KeyRing — the standard
// shape tests use when they need a stable HMAC across a parsec.New /
// recreate pair.
func RingFromSecret(t testing.TB, secret []byte) *auth.KeyRing {
	t.Helper()
	ring, err := auth.NewKeyRingFromSecret(secret)
	if err != nil {
		t.Fatalf("testutil: NewKeyRingFromSecret: %v", err)
	}
	return ring
}

// NewTestParsec boots a Parsec with an ephemeral random secret and a fast
// sweep interval. It returns the instance and a cleanup that cancels the
// goroutine and waits for Run to exit.
//
// The returned Parsec is guaranteed to have started its broker — the helper
// waits briefly so callers can publish immediately.
func NewTestParsec(t *testing.T) (*parsec.Parsec, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsec.New(parsec.Options{
		KeyRing:       RingFromSecret(t, secret),
		SweepInterval: 50 * time.Millisecond,
		Logger:        slog.New(slog.DiscardHandler),
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
	// The broker needs a few ms to actually start; publishes before then
	// hit the BrokerNotReady path. 75ms matches parsec_test.go.
	time.Sleep(75 * time.Millisecond)
	return p, func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
			t.Logf("warn: parsec.Run did not exit within 2s")
		}
	}
}

// NewTestService is NewTestParsec wrapped in a Service. The cleanup also
// shuts down the underlying parsec.
func NewTestService(t *testing.T) (*service.Service, func()) {
	t.Helper()
	p, stop := NewTestParsec(t)
	return service.New(p, Version), stop
}

// NewTestServer boots a Parsec + Service and mounts the full HTTP surface
// (websocket / Twirp / SSE / healthz / manifest) on an httptest.Server.
// Bearer enforcement is disabled — surface tests pass a Bearer header where
// the server cares, and the validator is nil so tests don't have to mint
// mgmt tokens.
//
// The cleanup tears down the http server and the parsec goroutine.
func NewTestServer(t *testing.T) (*httptest.Server, *service.Service, func()) {
	t.Helper()
	p, stop := NewTestParsec(t)
	svc := service.New(p, Version)
	srv := httptest.NewServer(server.New(p, svc, slog.New(slog.DiscardHandler), nil))
	cleanup := func() {
		srv.Close()
		stop()
	}
	return srv, svc, cleanup
}

// NewMetricsBearerTestServer boots a Parsec instance configured with the
// supplied metrics bearer token, mounts the full HTTP surface, and
// returns the server + token so tests can confirm /metrics is gated.
//
// The mgmt validator is disabled because /metrics has its own bearer
// pathway distinct from the JWT keyring.
func NewMetricsBearerTestServer(t *testing.T, metricsBearer string) (*httptest.Server, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsec.New(parsec.Options{
		KeyRing:            RingFromSecret(t, secret),
		SweepInterval:      50 * time.Millisecond,
		Logger:             slog.New(slog.DiscardHandler),
		MetricsBearerToken: metricsBearer,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(doneCh)
	}()
	time.Sleep(75 * time.Millisecond)
	svc := service.New(p, Version)
	srv := httptest.NewServer(server.New(p, svc, slog.New(slog.DiscardHandler), nil))
	return srv, func() {
		srv.Close()
		cancel()
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
			t.Logf("warn: parsec.Run did not exit within 2s")
		}
	}
}

// NewTestServerWithRevocations is NewTestServer plus a memory-backed
// tokenbroker.RevocationStore wired through parsec.Options. The store
// is returned so tests can inspect it directly (e.g. confirm a CLI
// RevokeToken call landed an entry).
func NewTestServerWithRevocations(t *testing.T) (*httptest.Server, *service.Service, *tokenbroker.MemoryRevocations, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	store := tokenbroker.NewMemoryRevocations()
	p, err := parsec.New(parsec.Options{
		KeyRing:         RingFromSecret(t, secret),
		SweepInterval:   50 * time.Millisecond,
		Logger:          slog.New(slog.DiscardHandler),
		RevocationStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(doneCh)
	}()
	time.Sleep(75 * time.Millisecond)
	svc := service.New(p, Version)
	srv := httptest.NewServer(server.New(p, svc, slog.New(slog.DiscardHandler), nil))
	cleanup := func() {
		srv.Close()
		cancel()
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
			t.Logf("warn: parsec.Run did not exit within 2s")
		}
	}
	return srv, svc, store, cleanup
}

// NewBearerTestServer is like NewTestServer but installs the real
// MgmtValidator. Returns a freshly minted mgmt bearer so the caller can
// build an authenticated rpc.Client.
func NewBearerTestServer(t *testing.T) (*httptest.Server, *service.Service, string, func()) {
	t.Helper()
	p, stop := NewTestParsec(t)
	svc := service.New(p, Version)
	token, _, err := p.Issuer().IssueMgmt("test-operator", time.Hour)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(p, svc, slog.New(slog.DiscardHandler), server.MgmtValidator(p)))
	cleanup := func() {
		srv.Close()
		stop()
	}
	return srv, svc, token, cleanup
}

// MustParseName parses s and fails the test if it doesn't validate. Used by
// surface tests that need a Name value rather than a string.
func MustParseName(t *testing.T, s string) channels.Name {
	t.Helper()
	n, err := channels.ParseName(s)
	if err != nil {
		t.Fatalf("ParseName(%q): %v", s, err)
	}
	return n
}

// Compile-time: this package depends on rpc to ensure imports stay aligned.
var _ = rpc.ParsecServicePathPrefix
