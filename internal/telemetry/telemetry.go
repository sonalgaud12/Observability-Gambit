// Package telemetry wires the OpenTelemetry SDK to OTLP/gRPC exporters for
// traces, metrics and logs (GenAI events are log records).
//
// Configuration comes from the standard OTEL_* environment variables
// (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_INSECURE,
// OTEL_RESOURCE_ATTRIBUTES, OTEL_SERVICE_NAME, OTEL_SDK_DISABLED), so the
// binary stays vendor-neutral: point it at any Collector.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Version is stamped into service.version.
const Version = "0.1.0"

// Setup installs global Tracer, Meter and Logger providers and returns a
// shutdown function that flushes them. When OTEL_SDK_DISABLED=true it installs
// nothing and the global no-op providers are used.
//
// Metrics and logs can be switched off on their own with
// OTEL_METRICS_EXPORTER=none / OTEL_LOGS_EXPORTER=none, for a Collector that
// only has a trace pipeline.
func Setup(ctx context.Context, extra ...attribute.KeyValue) (func(context.Context) error, error) {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return func(context.Context) error { return nil }, nil
	}

	if os.Getenv("OTEL_SERVICE_NAME") == "" {
		os.Setenv("OTEL_SERVICE_NAME", "chess-agent")
	}
	// Local demo default: plaintext gRPC to a Collector on localhost.
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	}

	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(append([]attribute.KeyValue{
			semconv.ServiceVersion(Version),
		}, extra...)...),
	)
	if err != nil && !errors.Is(err, resource.ErrSchemaURLConflict) {
		return nil, fmt.Errorf("resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		// Sample everything at the source; sampling decisions belong in the
		// Collector (tail_sampling), where the blunder label is known.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(2*time.Second)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	shutdowns := []func(context.Context) error{tp.Shutdown}

	if !strings.EqualFold(os.Getenv("OTEL_METRICS_EXPORTER"), "none") {
		mexp, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp metric exporter: %w", err)
		}
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp, sdkmetric.WithInterval(15*time.Second))),
		)
		otel.SetMeterProvider(mp)
		shutdowns = append(shutdowns, mp.Shutdown)
	}

	if !strings.EqualFold(os.Getenv("OTEL_LOGS_EXPORTER"), "none") {
		lexp, err := otlploggrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp log exporter: %w", err)
		}
		lp := sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(lexp)),
		)
		logglobal.SetLoggerProvider(lp)
		shutdowns = append(shutdowns, lp.Shutdown)
	}

	return func(ctx context.Context) error {
		var errs []error
		// Logs and metrics first: the trace exporter is the one the demo
		// cannot do without, so it gets whatever time is left last.
		for i := len(shutdowns) - 1; i >= 0; i-- {
			errs = append(errs, shutdowns[i](ctx))
		}
		return errors.Join(errs...)
	}, nil
}
