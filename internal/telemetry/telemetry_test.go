package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/telemetry"
)

// TestInit_EmptyEndpoint_DegradesSafely is the tracer bullet for criterion 6:
// with no OTLP endpoint configured (no backend assumed to exist), Init must
// not error and must not crash — it returns a working SDK whose
// TracerProvider/MeterProvider can still be used (e.g. to obtain trace_id/
// span_id for log correlation) even though nothing is exported anywhere.
func TestInit_EmptyEndpoint_DegradesSafely(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sdk, err := telemetry.Init(ctx, telemetry.Config{
		ServiceName:  "change-tracking-dashboard",
		OTLPEndpoint: "",
	})
	if err != nil {
		t.Fatalf("Init with empty endpoint returned an error, want safe degrade: %v", err)
	}
	if sdk == nil {
		t.Fatal("Init returned a nil SDK")
	}

	// Even with no exporter, a real span/trace ID must still be produced —
	// log correlation must keep working with no backend.
	_, span := sdk.TracerProvider.Tracer("test").Start(ctx, "op")
	if !span.SpanContext().IsValid() {
		t.Error("span produced by the degraded SDK has an invalid SpanContext")
	}
	span.End()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := sdk.Shutdown(shutdownCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// TestInit_SetsServiceNameResourceAttribute verifies the service.name
// resource attribute required by the observability standard is attached to
// the SDK's resource.
func TestInit_SetsServiceNameResourceAttribute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sdk, err := telemetry.Init(ctx, telemetry.Config{ServiceName: "change-tracking-dashboard"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = sdk.Shutdown(context.Background()) }()

	found := false
	for _, kv := range sdk.Resource.Attributes() {
		if string(kv.Key) == "service.name" && kv.Value.AsString() == "change-tracking-dashboard" {
			found = true
		}
	}
	if !found {
		t.Errorf("resource attributes missing service.name=change-tracking-dashboard: %v", sdk.Resource.Attributes())
	}
}

// TestInit_WithConfiguredEndpoint_DoesNotBlockOrError verifies Init with a
// non-empty (but unreachable) OTLP endpoint still returns promptly without
// error — the gRPC exporter connects lazily, no backend is assumed to exist
// at runtime. Shutdown is bounded by the deadline passed to it (it may
// legitimately return an error when the final flush can't reach a backend
// that was never there — that error must be surfaced to the caller to log,
// never swallowed — but it must not hang past the deadline or panic).
func TestInit_WithConfiguredEndpoint_DoesNotBlockOrError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sdk, err := telemetry.Init(ctx, telemetry.Config{
		ServiceName:  "change-tracking-dashboard",
		OTLPEndpoint: "127.0.0.1:1", // deliberately unreachable, never dialed synchronously
	})
	if err != nil {
		t.Fatalf("Init with a configured (unreachable) endpoint returned an error: %v", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()

	done := make(chan error, 1)
	go func() { done <- sdk.Shutdown(shutdownCtx) }()

	select {
	case <-done:
		// Returned (with or without an error) within the deadline — that's
		// the safe-degrade contract; an export error to an absent backend
		// is expected and acceptable here, a hang is not.
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return within its bounded deadline")
	}
}

// TestResolveOTLPEndpoint_EnvVarTakesPrecedenceOverConfig verifies the
// documented precedence: OTEL_EXPORTER_OTLP_ENDPOINT wins over
// observability.otlp_endpoint when both are set.
func TestResolveOTLPEndpoint_EnvVarTakesPrecedenceOverConfig(t *testing.T) {
	got := telemetry.ResolveOTLPEndpoint("env-endpoint:4317", "config-endpoint:4317")
	if got != "env-endpoint:4317" {
		t.Errorf("ResolveOTLPEndpoint = %q, want env value to win", got)
	}
}

// TestResolveOTLPEndpoint_FallsBackToConfig_WhenEnvUnset verifies the config
// path works when the env var is not set.
func TestResolveOTLPEndpoint_FallsBackToConfig_WhenEnvUnset(t *testing.T) {
	got := telemetry.ResolveOTLPEndpoint("", "config-endpoint:4317")
	if got != "config-endpoint:4317" {
		t.Errorf("ResolveOTLPEndpoint = %q, want config value", got)
	}
}

// TestResolveOTLPEndpoint_EmptyWhenNeitherSet verifies the safe-degrade
// input case: neither source configured yields an empty endpoint (Init must
// treat this as "no backend").
func TestResolveOTLPEndpoint_EmptyWhenNeitherSet(t *testing.T) {
	if got := telemetry.ResolveOTLPEndpoint("", ""); got != "" {
		t.Errorf("ResolveOTLPEndpoint = %q, want empty", got)
	}
}
