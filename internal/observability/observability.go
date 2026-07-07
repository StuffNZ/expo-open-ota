// Package observability wires optional OpenTelemetry export for the three
// signals (traces, metrics, logs). It is entirely opt-in via OTEL_ENABLED:
// when unset or not "true", Setup is a no-op and the server behaves exactly
// as before (Prometheus /metrics endpoint, stdlib log output).
//
// When enabled:
//   - Traces are exported over OTLP and HTTP requests are instrumented via
//     otelmux (see router).
//   - Metrics: the existing Prometheus registry is bridged to OTLP, so the
//     business metrics in internal/metrics are exported unchanged while the
//     /metrics endpoint keeps working for existing scrapers.
//   - Logs: slog's default handler fans out to stdout and OTLP. The stdlib
//     log package routes through slog's default handler, so every existing
//     log.Printf call site is exported without modification.
//
// Endpoint, headers, sampling and resource attributes follow the standard
// OpenTelemetry environment variables (OTEL_EXPORTER_OTLP_ENDPOINT,
// OTEL_EXPORTER_OTLP_HEADERS, OTEL_TRACES_SAMPLER, OTEL_RESOURCE_ATTRIBUTES,
// ...), which the SDK reads natively — any OTLP-compatible backend (SigNoz,
// New Relic, Grafana, an OTel Collector) works without code changes.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"

	prombridge "go.opentelemetry.io/contrib/bridges/prometheus"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
)

const defaultServiceName = "expo-open-ota"

func Enabled() bool {
	return strings.EqualFold(os.Getenv("OTEL_ENABLED"), "true")
}

func ServiceName() string {
	if name := os.Getenv("OTEL_SERVICE_NAME"); name != "" {
		return name
	}
	return defaultServiceName
}

// Setup configures the global OpenTelemetry providers and returns a shutdown
// function that flushes pending telemetry. When OTEL_ENABLED is not "true" it
// does nothing and returns a no-op shutdown.
func Setup(ctx context.Context) (func(context.Context) error, error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}

	traceExporter, metricExporter, logExporter, err := newExporters(ctx)
	if err != nil {
		return nil, err
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(ServiceName()),
	))
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// The Prometheus bridge re-exports everything registered on the default
	// registry (the business metrics in internal/metrics plus Go runtime
	// collectors) over OTLP, so the same series reach both scrapers and OTLP
	// backends.
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			metricExporter,
			sdkmetric.WithProducer(prombridge.NewMetricProducer()),
		)),
	)
	otel.SetMeterProvider(meterProvider)

	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)
	global.SetLoggerProvider(loggerProvider)

	// slog.SetDefault also reroutes the stdlib log package through this
	// handler, so existing log.Printf call sites reach OTLP too.
	slog.SetDefault(slog.New(fanoutHandler{
		slog.NewTextHandler(os.Stdout, nil),
		otelslog.NewHandler(ServiceName(), otelslog.WithLoggerProvider(loggerProvider)),
	}))

	return func(ctx context.Context) error {
		return errors.Join(
			tracerProvider.Shutdown(ctx),
			meterProvider.Shutdown(ctx),
			loggerProvider.Shutdown(ctx),
		)
	}, nil
}

func newExporters(ctx context.Context) (sdktrace.SpanExporter, sdkmetric.Exporter, sdklog.Exporter, error) {
	protocol := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	switch protocol {
	case "", "http/protobuf":
		traceExporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		metricExporter, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("otlp metric exporter: %w", err)
		}
		logExporter, err := otlploghttp.New(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("otlp log exporter: %w", err)
		}
		return traceExporter, metricExporter, logExporter, nil
	case "grpc":
		traceExporter, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		metricExporter, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("otlp metric exporter: %w", err)
		}
		logExporter, err := otlploggrpc.New(ctx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("otlp log exporter: %w", err)
		}
		return traceExporter, metricExporter, logExporter, nil
	default:
		return nil, nil, nil, fmt.Errorf("unsupported OTEL_EXPORTER_OTLP_PROTOCOL %q (expected \"http/protobuf\" or \"grpc\")", protocol)
	}
}

// Infof logs a request-scoped message. With OTel enabled it goes through slog
// with the request context so the OTLP record carries trace/span ids; when
// disabled it is a plain log.Printf, keeping the historical output format.
func Infof(ctx context.Context, format string, args ...any) {
	if !Enabled() {
		log.Printf(format, args...)
		return
	}
	slog.Default().InfoContext(ctx, fmt.Sprintf(format, args...))
}

// Errorf is Infof at error level.
func Errorf(ctx context.Context, format string, args ...any) {
	if !Enabled() {
		log.Printf(format, args...)
		return
	}
	slog.Default().ErrorContext(ctx, fmt.Sprintf(format, args...))
}

// fanoutHandler duplicates records to every wrapped handler (stdout + OTLP).
type fanoutHandler []slog.Handler

func (h fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	var errs []error
	for _, handler := range h {
		if handler.Enabled(ctx, record.Level) {
			errs = append(errs, handler.Handle(ctx, record.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (h fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make(fanoutHandler, len(h))
	for i, handler := range h {
		next[i] = handler.WithAttrs(attrs)
	}
	return next
}

func (h fanoutHandler) WithGroup(name string) slog.Handler {
	next := make(fanoutHandler, len(h))
	for i, handler := range h {
		next[i] = handler.WithGroup(name)
	}
	return next
}
