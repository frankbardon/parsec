package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec/internal/cli"
	"github.com/frankbardon/parsec/internal/testutil"
	"github.com/frankbardon/parsec/sinks"
)

func TestDLQList_RequiresSinkName(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	_, err := runCmd(t, cli.DLQCommand(), "dlq", "--server", srv.URL, "list")
	if err == nil {
		t.Fatal("expected error when sink name missing")
	}
}

func TestDLQList_AgainstFakeServer(t *testing.T) {
	srv, svc, stop := testutil.NewTestServer(t)
	defer stop()
	// Push an item into the live DLQ so list returns something.
	_ = svc.Parsec().DLQ().Push(context.Background(), sinks.DLQItem{
		Sink:    "email",
		At:      time.Now(),
		Message: sinks.Message{Subject: "hi"},
	})
	out, err := runCmd(t, cli.DLQCommand(), "dlq", "--server", srv.URL, "list", "email")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.dlq.list") {
		t.Errorf("expected envelope kind, got %s", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("expected subject in output, got %s", out)
	}
}

func TestDLQCount_AgainstFakeServer(t *testing.T) {
	srv, svc, stop := testutil.NewTestServer(t)
	defer stop()
	_ = svc.Parsec().DLQ().Push(context.Background(), sinks.DLQItem{Sink: "email", Message: sinks.Message{Subject: "a"}})
	_ = svc.Parsec().DLQ().Push(context.Background(), sinks.DLQItem{Sink: "email", Message: sinks.Message{Subject: "b"}})
	out, err := runCmd(t, cli.DLQCommand(), "dlq", "--server", srv.URL, "count", "email")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.dlq.count") {
		t.Errorf("expected count envelope, got %s", out)
	}
}

func TestDLQCount_RequiresSinkName(t *testing.T) {
	_, err := runCmd(t, cli.DLQCommand(), "dlq", "count")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDLQDiscard_AgainstFakeServer(t *testing.T) {
	srv, svc, stop := testutil.NewTestServer(t)
	defer stop()
	_ = svc.Parsec().DLQ().Push(context.Background(), sinks.DLQItem{Sink: "x", Message: sinks.Message{Subject: "n"}})
	items, _ := svc.Parsec().DLQ().Items(context.Background(), "x", 1)
	out, err := runCmd(t, cli.DLQCommand(), "dlq", "--server", srv.URL, "discard", items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.dlq.discarded") {
		t.Errorf("expected discarded envelope, got %s", out)
	}
}

func TestDLQDiscard_RequiresID(t *testing.T) {
	_, err := runCmd(t, cli.DLQCommand(), "dlq", "discard")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDLQReplay_RequiresID(t *testing.T) {
	_, err := runCmd(t, cli.DLQCommand(), "dlq", "replay")
	if err == nil {
		t.Fatal("expected error")
	}
}
