package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// Config configures Init. ServiceName becomes the "service.name" resource
// attribute on every span and metric point. OTLPEndpoint is the OTLP gRPC
// target ("host:port"); an empty value means "no backend configured" — Init
// must degrade safely, not crash or block.
type Config struct {
	ServiceName  string
	OTLPEndpoint string
}

// SDK bundles the process-wide TracerProvider and MeterProvider Init sets
// up, plus the Resource they share. main.go registers them as the global
// providers (so any package using the plain otel.Tracer/otel.Meter
// accessors picks them up) and calls Shutdown once on exit to flush
// pending spans/metrics and release exporter connections.
type SDK struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *metric.MeterProvider
	Resource       *resource.Resource
}

// ResolveOTLPEndpoint applies the documented precedence for the OTLP
// endpoint: the standard OTEL_EXPORTER_OTLP_ENDPOINT environment variable,
// when set, always wins over the app's own observability.otlp_endpoint
// config value (OTel convention: env vars override in-process config).
// Both empty yields "" — Init's safe-degrade input.
func ResolveOTLPEndpoint(envValue, configValue string) string {
	if envValue != "" {
		return envValue
	}
	return configValue
}

// Init initializes the OTel SDK once at startup: a Resource carrying
// service.name, and a TracerProvider + MeterProvider wired to OTLP-over-gRPC
// exporters when cfg.OTLPEndpoint is non-empty. When it is empty, the
// returned providers are still fully functional (spans/metrics carry real
// IDs, usable for log correlation) but export nothing anywhere — no backend
// is assumed to exist, and a missing endpoint must never crash the process.
//
// Exporter construction is bounded by ctx; callers should pass a
// context with a timeout so a slow/unreachable collector cannot hang
// startup indefinitely.
func Init(ctx context.Context, cfg Config) (*SDK, error) {
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry: build resource: %w", err)
	}

	if cfg.OTLPEndpoint == "" {
		return &SDK{
			TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithResource(res)),
			MeterProvider:  metric.NewMeterProvider(metric.WithResource(res)),
			Resource:       res,
		}, nil
	}

	endpoint := normalizeEndpoint(cfg.OTLPEndpoint)

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create OTLP trace exporter: %w", err)
	}

	metricExporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create OTLP metric exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExporter),
	)
	mp := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
	)

	return &SDK{TracerProvider: tp, MeterProvider: mp, Resource: res}, nil
}

// Shutdown flushes and closes both providers, giving each up to ctx's
// deadline. Errors from both are joined so a caller sees every failure, not
// just the first.
func (s *SDK) Shutdown(ctx context.Context) error {
	var errs []error
	if err := s.TracerProvider.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("telemetry: shutdown tracer provider: %w", err))
	}
	if err := s.MeterProvider.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("telemetry: shutdown meter provider: %w", err))
	}
	return errors.Join(errs...)
}

// normalizeEndpoint strips a scheme prefix if present: the config/env value
// follows the standard OTEL_EXPORTER_OTLP_ENDPOINT convention (may include
// "http://"/"https://"), but otlptracegrpc/otlpmetricgrpc's WithEndpoint
// expects a bare "host:port".
func normalizeEndpoint(endpoint string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(endpoint, prefix) {
			return strings.TrimPrefix(endpoint, prefix)
		}
	}
	return endpoint
}
