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
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Config configures Init. ServiceName becomes the "service.name" resource
// attribute on every span and metric point. OTLPEndpoint is the OTLP gRPC
// target ("host:port"); an empty value means "no backend configured" — Init
// must degrade safely, not crash or block.
type Config struct {
	ServiceName  string
	OTLPEndpoint string

	// Headers are sent on every OTLP export request. This is how a
	// managed backend is authenticated against: Honeycomb expects an
	// "x-honeycomb-team" header carrying the ingest API key. Empty (the
	// local-collector case) sends no headers.
	//
	// A backend that requires auth and does not get it typically rejects
	// exports silently, so an endpoint that implies auth without headers
	// is worth noticing at startup — see Init's returned SDK.
	Headers map[string]string
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

	endpoint, secure := parseEndpoint(cfg.OTLPEndpoint)

	traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
	metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(endpoint)}
	if !secure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	}
	// The secure case adds nothing: the gRPC exporters default to TLS
	// against the host's root CA pool, which is what a managed endpoint
	// (api.honeycomb.io:443) requires. WithInsecure is the opt-out.
	if len(cfg.Headers) > 0 {
		traceOpts = append(traceOpts, otlptracegrpc.WithHeaders(cfg.Headers))
		metricOpts = append(metricOpts, otlpmetricgrpc.WithHeaders(cfg.Headers))
	}

	traceExporter, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create OTLP trace exporter: %w", err)
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, metricOpts...)
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

// tlsPortSuffix is the port that, on a scheme-less endpoint, means "this is
// a TLS endpoint" — the form managed backends are configured as
// ("api.honeycomb.io:443").
const tlsPortSuffix = ":443"

// parseEndpoint splits the configured endpoint into the bare "host:port"
// otlptracegrpc/otlpmetricgrpc's WithEndpoint expects, and whether to dial it
// over TLS.
//
// The config/env value follows the standard OTEL_EXPORTER_OTLP_ENDPOINT
// convention, so it may or may not carry a scheme. An explicit scheme decides:
// "https://" is TLS, "http://" is not. Without one, port 443 is taken as TLS
// and anything else (the in-cluster collector on :4317) as plaintext — so
// both "https://api.honeycomb.io" and "api.honeycomb.io:443" reach Honeycomb,
// and an existing "collector:4317" value keeps working untouched.
func parseEndpoint(endpoint string) (hostPort string, secure bool) {
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		return strings.TrimPrefix(endpoint, "https://"), true
	case strings.HasPrefix(endpoint, "http://"):
		return strings.TrimPrefix(endpoint, "http://"), false
	default:
		return endpoint, strings.HasSuffix(endpoint, tlsPortSuffix)
	}
}

// ResolveOTLPHeaders applies the documented precedence for OTLP export
// headers, mirroring ResolveOTLPEndpoint: the standard
// OTEL_EXPORTER_OTLP_HEADERS environment variable, when set, wins outright.
// Otherwise a non-empty Honeycomb ingest key (HONEYCOMB_API_KEY) becomes the
// single "x-honeycomb-team" header Honeycomb authenticates on.
//
// Both empty yields nil — Init's no-auth path, correct for a local collector.
// The key is deliberately env-only: it is a credential, and belongs in a
// Kubernetes Secret alongside GITHUB_APP_PRIVATE_KEY_FILE, never in the
// tracker ConfigMap.
func ResolveOTLPHeaders(envHeaders, honeycombAPIKey string) map[string]string {
	if envHeaders != "" {
		return parseHeaderList(envHeaders)
	}
	if honeycombAPIKey != "" {
		return map[string]string{honeycombTeamHeader: honeycombAPIKey}
	}
	return nil
}

// honeycombTeamHeader is the header Honeycomb reads the ingest API key from.
const honeycombTeamHeader = "x-honeycomb-team"

// parseHeaderList parses the W3C-Baggage-shaped value of
// OTEL_EXPORTER_OTLP_HEADERS: comma-separated "key=value" pairs, e.g.
// "x-honeycomb-team=abc123,x-honeycomb-dataset=metrics". Malformed entries
// (no "=") and blank keys are skipped rather than failing startup: a
// mistyped header must not take the process down, and the resulting
// export rejection is visible at the backend.
func parseHeaderList(raw string) map[string]string {
	headers := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		headers[key] = strings.TrimSpace(value)
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}
