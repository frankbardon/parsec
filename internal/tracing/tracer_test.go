package tracing_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/frankbardon/parsec/internal/tracing"
)

// TestNewTracer_NoOpWhenEndpointEmpty verifies the default-off path
// returns a tracer that produces invalid span contexts (no exporter
// configured, no allocations on the hot path).
func TestNewTracer_NoOpWhenEndpointEmpty(t *testing.T) {
	tr, shutdown, err := tracing.NewTracer("")
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	if tr == nil {
		t.Fatal("tracer is nil")
	}
	if shutdown == nil {
		t.Fatal("shutdown is nil")
	}
	ctx, span := tr.Start(context.Background(), "test")
	defer span.End()
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		t.Errorf("no-op tracer produced a valid SpanContext: %v", sc)
	}
	ctx2, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := shutdown(ctx2); err != nil {
		t.Errorf("noop shutdown returned error: %v", err)
	}
}

// TestNewTracer_RealExporterWhenEndpointSet checks that supplying an
// endpoint (even one that nothing listens to) doesn't fail at
// construction. The OTLP exporter is lazy — it does not connect during
// New, only when the first span is exported.
func TestNewTracer_RealExporterWhenEndpointSet(t *testing.T) {
	tr, shutdown, err := tracing.NewTracer("127.0.0.1:14318")
	if err != nil {
		t.Fatalf("NewTracer with endpoint: %v", err)
	}
	if tr == nil {
		t.Fatal("tracer is nil")
	}
	ctx, span := tr.Start(context.Background(), "smoke")
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		t.Errorf("real tracer produced invalid SpanContext")
	}
	span.End()
	sctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// Shutdown may time out trying to flush against a dead collector;
	// we accept either nil or a non-nil error, what matters is that it
	// returns. A panic / hang would fail the test.
	_ = shutdown(sctx)
}
