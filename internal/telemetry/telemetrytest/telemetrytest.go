// Package telemetrytest installs in-memory trace, metric and log providers
// as the globals, so tests can check the spans, metrics and GenAI events a
// piece of code emits.
//
// Build the code under test (llm.Instrument, agent.New, a game player) after
// calling Install: they bind to the global providers when constructed.
package telemetrytest

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// Recorder holds what the code under test emitted.
type Recorder struct {
	Spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
	logs   *logStore
}

// Install sets in-memory providers as the globals for the rest of the test.
func Install(t testing.TB) *Recorder {
	t.Helper()
	r := &Recorder{
		Spans:  tracetest.NewSpanRecorder(),
		reader: sdkmetric.NewManualReader(),
		logs:   &logStore{},
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r.Spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(r.reader))
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(r.logs)))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	logglobal.SetLoggerProvider(lp)
	t.Cleanup(func() {
		ctx := context.Background()
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
	})
	return r
}

// SpanAttrs returns the attributes of every ended span with this name.
func (r *Recorder) SpanAttrs(name string) []map[string]attribute.Value {
	var out []map[string]attribute.Value
	for _, s := range r.Spans.Ended() {
		if s.Name() != name {
			continue
		}
		m := map[string]attribute.Value{}
		for _, kv := range s.Attributes() {
			m[string(kv.Key)] = kv.Value
		}
		out = append(out, m)
	}
	return out
}

// Span returns the span context of the first ended span with this name.
func (r *Recorder) Span(name string) (trace.SpanContext, bool) {
	for _, s := range r.Spans.Ended() {
		if s.Name() == name {
			return s.SpanContext(), true
		}
	}
	return trace.SpanContext{}, false
}

// Metric collects the metrics now and returns the one with this name.
func (r *Recorder) Metric(t testing.TB, name string) (metricdata.Metrics, bool) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// Event is one recorded GenAI event.
type Event struct {
	Name     string
	Severity log.Severity
	TraceID  trace.TraceID
	SpanID   trace.SpanID
	Attrs    map[string]log.Value
}

// Events returns the recorded events with this name.
func (r *Recorder) Events(name string) []Event {
	r.logs.mu.Lock()
	defer r.logs.mu.Unlock()
	var out []Event
	for _, e := range r.logs.events {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

type logStore struct {
	mu     sync.Mutex
	events []Event
}

func (s *logStore) Export(_ context.Context, records []sdklog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range records {
		rec := &records[i]
		e := Event{
			Name: rec.EventName(), Severity: rec.Severity(),
			TraceID: rec.TraceID(), SpanID: rec.SpanID(),
			Attrs: map[string]log.Value{},
		}
		rec.WalkAttributes(func(kv log.KeyValue) bool {
			e.Attrs[kv.Key] = kv.Value
			return true
		})
		s.events = append(s.events, e)
	}
	return nil
}

func (s *logStore) Shutdown(context.Context) error   { return nil }
func (s *logStore) ForceFlush(context.Context) error { return nil }
