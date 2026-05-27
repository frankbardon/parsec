package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/frankbardon/parsec/internal/testutil"
	"github.com/frankbardon/parsec/sinks"
)

func TestService_DLQList_ReturnsItems(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	dlq := svc.Parsec().DLQ()
	if err := dlq.Push(context.Background(), sinks.DLQItem{
		Sink:    "email",
		At:      time.Now(),
		Message: sinks.Message{Subject: "hi", Body: "b"},
	}); err != nil {
		t.Fatal(err)
	}
	listing, err := svc.ListDLQ(context.Background(), "email", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(listing.Items))
	}
	if listing.Items[0].Subject != "hi" {
		t.Errorf("Subject = %q", listing.Items[0].Subject)
	}
}

func TestService_DLQCountAndDiscard(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	dlq := svc.Parsec().DLQ()
	_ = dlq.Push(context.Background(), sinks.DLQItem{Sink: "email", Message: sinks.Message{Subject: "a"}})
	_ = dlq.Push(context.Background(), sinks.DLQItem{Sink: "email", Message: sinks.Message{Subject: "b"}})

	n, err := svc.CountDLQ(context.Background(), "email")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
	listing, _ := svc.ListDLQ(context.Background(), "email", 5)
	if err := svc.DiscardDLQ(context.Background(), listing.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	n, _ = svc.CountDLQ(context.Background(), "email")
	if n != 1 {
		t.Errorf("Count after discard = %d, want 1", n)
	}
}
