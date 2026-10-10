// Package telemetry wires the OpenTelemetry SDK to an OTLP/gRPC exporter.
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
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Version is stamped into service.version.
const Version = "0.1.0"

// Setup installs a global TracerProvider and returns a shutdown function that
// flushes pending spans. When OTEL_SDK_DISABLED=true it installs nothing and
// the global no-op provider is used.
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
		return nil, fmt.Errorf("otlp exporter: %w", err)
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

	return tp.Shutdown, nil
}

// CaptureContent reports whether prompt/completion content should be recorded
// on spans. Off by default, matching the GenAI instrumentation convention.
func CaptureContent() bool {
	return strings.EqualFold(os.Getenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"), "true")
}
