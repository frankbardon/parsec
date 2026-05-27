package rpcclient_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec/internal/rpcclient"
	"github.com/frankbardon/parsec/internal/testutil"
)

// newClient builds a CLI rpcclient against a fresh in-process HTTP server.
// Returns the client and a cleanup func that tears everything down.
func newClient(t *testing.T) (*rpcclient.Client, func()) {
	t.Helper()
	srv, _, stop := testutil.NewTestServer(t)
	return rpcclient.New(srv.URL, ""), stop
}

func TestClient_OpenPublic(t *testing.T) {
	c, stop := newClient(t)
	defer stop()
	got, err := c.OpenPublic(t.Context(), "public:test.x.y", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "public:test.x.y" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.State != "open" {
		t.Errorf("State = %q", got.State)
	}
	if got.TTL != time.Minute.String() {
		t.Errorf("TTL = %q", got.TTL)
	}
}

func TestClient_ListAndGetAndDelete(t *testing.T) {
	c, stop := newClient(t)
	defer stop()
	ctx := t.Context()
	if _, err := c.OpenPublic(ctx, "public:test.x.y", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenPublic(ctx, "public:test.x.z", time.Minute); err != nil {
		t.Fatal(err)
	}
	list, err := c.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
	got, err := c.GetChannel(ctx, "public:test.x.y")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "public:test.x.y" {
		t.Errorf("Get returned %q", got.Name)
	}
	if err := c.DeleteChannel(ctx, "public:test.x.y"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetChannel(ctx, "public:test.x.y"); err == nil {
		t.Fatal("expected GetChannel after delete to fail")
	}
}

func TestClient_CreatePrivateAndRefresh(t *testing.T) {
	c, stop := newClient(t)
	defer stop()
	ctx := t.Context()
	creds, err := c.CreatePrivate(ctx, "user-1", "private:test.user.1.notif", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken == "" || creds.RefreshToken == "" {
		t.Fatal("missing tokens")
	}
	if !strings.HasPrefix(creds.Name, "private:") {
		t.Errorf("Name = %q", creds.Name)
	}
	res, err := c.RefreshToken(ctx, creds.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if res.AccessToken == "" {
		t.Fatal("refreshed access token empty")
	}
}

func TestClient_Publish(t *testing.T) {
	c, stop := newClient(t)
	defer stop()
	ctx := t.Context()
	if _, err := c.OpenPublic(ctx, "public:test.pub.demo", time.Minute); err != nil {
		t.Fatal(err)
	}
	ack, err := c.Publish(ctx, "public:test.pub.demo", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if ack.Offset == 0 {
		t.Errorf("Offset = 0")
	}
	if ack.Epoch == "" {
		t.Errorf("Epoch empty")
	}
}

func TestClient_PublishUnknownChannel(t *testing.T) {
	c, stop := newClient(t)
	defer stop()
	_, err := c.Publish(t.Context(), "public:test.pub.demo", []byte("x"))
	if err == nil {
		t.Fatal("expected error for unknown channel")
	}
}

func TestClient_OpenPublicInvalidName(t *testing.T) {
	c, stop := newClient(t)
	defer stop()
	_, err := c.OpenPublic(t.Context(), "not-a-channel", time.Minute)
	if err == nil {
		t.Fatal("expected error for bad name")
	}
}

func TestClient_SubscribeNDJSON_BadName(t *testing.T) {
	c, stop := newClient(t)
	defer stop()
	// SSE rejects bad channel name with 400 → returns error from the
	// HTTP status guard.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := c.SubscribeNDJSON(ctx, "not-a-channel", noopWriter{})
	if err == nil {
		t.Fatal("expected error for bad channel")
	}
	if !strings.Contains(err.Error(), "subscribe") {
		t.Errorf("err message = %v", err)
	}
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
