package metrics

import (
	"context"
	"time"

	"github.com/frankbardon/parsec/sinks"
)

// sinkObserver wraps a sinks.Sink with a metrics envelope. Every Send
// records duration into SinkDuration and increments SinkAttemptsTotal
// with success / failure. The wrapped sink's Name() flows through
// unchanged so the registry key stays stable.
type sinkObserver struct {
	inner sinks.Sink
	m     *Metrics
}

// WrapSink returns a sinks.Sink that observes Send calls into m. When m
// is nil, inner is returned unchanged.
func WrapSink(inner sinks.Sink, m *Metrics) sinks.Sink {
	if m == nil || inner == nil {
		return inner
	}
	return &sinkObserver{inner: inner, m: m}
}

// Name passes through.
func (s *sinkObserver) Name() string { return s.inner.Name() }

// Send wraps the inner call with timing + counter increments.
func (s *sinkObserver) Send(ctx context.Context, to sinks.Recipient, msg sinks.Message) error {
	start := time.Now()
	err := s.inner.Send(ctx, to, msg)
	s.m.SinkDuration.WithLabelValues(s.inner.Name()).Observe(time.Since(start).Seconds())
	result := ResultSuccess
	if err != nil {
		result = ResultFailure
	}
	s.m.SinkAttemptsTotal.WithLabelValues(s.inner.Name(), result).Inc()
	return err
}
