package telemetry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/telemetry"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestWithSpan_Success_SetsOKStatusNoRecordedError is the tracer bullet for
// the downstream git/store call seam: a successful call produces exactly one
// span, named after the call, with no error recorded on it.
func TestWithSpan_Success_SetsOKStatusNoRecordedError(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	err := telemetry.WithSpan(context.Background(), tracer, "store.get_high_water_mark", func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("WithSpan: %v", err)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	got := spans[0]
	if got.Name != "store.get_high_water_mark" {
		t.Errorf("span name = %q, want store.get_high_water_mark", got.Name)
	}
	if got.Status.Code == codes.Error {
		t.Errorf("span status = Error, want non-error on success")
	}
	if len(got.Events) != 0 {
		t.Errorf("expected no recorded exception events on success, got %d", len(got.Events))
	}
}

// TestWithSpan_Failure_RecordsExceptionAndSetsErrorStatus verifies criterion
// 5: a failing downstream call records the exception on its span and marks
// the span status as Error, and the original error is still returned to the
// caller unwrapped-changed (WithSpan is additive instrumentation only).
func TestWithSpan_Failure_RecordsExceptionAndSetsErrorStatus(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	wantErr := errors.New("walk commits: boom")
	err := telemetry.WithSpan(context.Background(), tracer, "gitsource.walk_commits", func(ctx context.Context) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithSpan returned %v, want %v", err, wantErr)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	got := spans[0]
	if got.Status.Code != codes.Error {
		t.Errorf("span status = %v, want Error", got.Status.Code)
	}
	if len(got.Events) == 0 {
		t.Fatalf("expected a recorded exception event on failure, got none")
	}
	if got.Events[0].Name != "exception" {
		t.Errorf("event name = %q, want exception", got.Events[0].Name)
	}
}
