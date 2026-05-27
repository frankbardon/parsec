// Package tracing wraps the OpenTelemetry SDK into a small surface
// Parsec actually uses: a tracer + a shutdown hook.
//
// Default-off: when no OTLP endpoint is configured, NewTracer returns a
// no-op tracer. The hot path performs no allocations and no exporter is
// constructed, so embedders pay nothing for the feature until they opt in.
package tracing

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// ServiceName is baked into every span resource. Embedders that need a
// different value should fork this package; we keep it constant so the
// trace UI always shows the same service name across deployments.
const ServiceName = "parsec"

// ShutdownFunc shuts the tracer provider down, flushing any buffered
// spans. Safe to call multiple times; no-op tracers return nil.
type ShutdownFunc func(context.Context) error

// NewTracer constructs a tracer + shutdown hook.
//
// When otlpEndpoint is "" the returned tracer is a no-op (every span is
// a zero-allocation stub) and the shutdown returns nil. This is the
// default-off path.
//
// When otlpEndpoint is set, an OTLP HTTP exporter is configured against
// that endpoint with insecure transport (the operator is responsible for
// terminating TLS at the collector or using a sidecar). The endpoint
// format follows the OTLP HTTP convention — host:port WITHOUT a scheme,
// or a full URL whose path will be honored.
func NewTracer(otlpEndpoint string) (trace.Tracer, ShutdownFunc, error) {
	if otlpEndpoint == "" {
		tp := noop.NewTracerProvider()
		return tp.Tracer(ServiceName), func(context.Context) error { return nil }, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(otlpEndpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, nil, errors.Join(errors.New("tracing: build OTLP exporter"), err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(ServiceName)),
	)
	if err != nil {
		return nil, nil, errors.Join(errors.New("tracing: build resource"), err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	tracer := tp.Tracer(ServiceName)
	shutdown := func(ctx context.Context) error {
		return tp.Shutdown(ctx)
	}
	return tracer, shutdown, nil
}
