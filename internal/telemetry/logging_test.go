package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestNewLogger_EmitsSingleLineJSONWithRequiredFields is the tracer bullet:
// every log line the structured logger emits is valid single-line JSON
// carrying at least timestamp, level, and message.
func TestNewLogger_EmitsSingleLineJSONWithRequiredFields(t *testing.T) {
	var buf bytes.Buffer
	logger := telemetry.NewLogger("change-tracking-dashboard", &buf)

	logger.Info("hello world")

	line := strings.TrimSpace(buf.String())
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("expected a single line, got: %q", buf.String())
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		t.Fatalf("log line is not valid JSON: %v; line: %s", err, line)
	}

	for _, key := range []string{"timestamp", "level", "message"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("log line missing required field %q; got: %s", key, line)
		}
	}
	if fields["message"] != "hello world" {
		t.Errorf("message = %v, want %q", fields["message"], "hello world")
	}
	if svc, _ := fields["service.name"].(string); svc != "change-tracking-dashboard" {
		t.Errorf("service.name = %v, want change-tracking-dashboard", fields["service.name"])
	}
}

// TestFromContext_WithActiveSpan_InjectsTraceAndSpanID verifies that a
// logger derived from a context carrying an active OTel span attaches
// trace_id and span_id to every log line emitted through it.
func TestFromContext_WithActiveSpan_InjectsTraceAndSpanID(t *testing.T) {
	var buf bytes.Buffer
	base := telemetry.NewLogger("svc", &buf)

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	correlated := telemetry.FromContext(ctx, base)
	correlated.Info("within span")

	var fields map[string]any
	if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}

	sc := span.SpanContext()
	if fields["trace_id"] != sc.TraceID().String() {
		t.Errorf("trace_id = %v, want %s", fields["trace_id"], sc.TraceID().String())
	}
	if fields["span_id"] != sc.SpanID().String() {
		t.Errorf("span_id = %v, want %s", fields["span_id"], sc.SpanID().String())
	}
}

// TestFromContext_NoActiveSpan_ReturnsBaseLoggerUnchanged verifies that
// deriving a logger from a context with no active span does not fabricate
// trace/span IDs.
func TestFromContext_NoActiveSpan_ReturnsBaseLoggerUnchanged(t *testing.T) {
	var buf bytes.Buffer
	base := telemetry.NewLogger("svc", &buf)

	logger := telemetry.FromContext(context.Background(), base)
	logger.Info("no span here")

	var fields map[string]any
	if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if _, ok := fields["trace_id"]; ok {
		t.Errorf("trace_id should be absent with no active span; got: %v", fields["trace_id"])
	}
}

// TestLoggerFromContext_FallsBackToDefault_WhenNoneStored verifies handlers
// that retrieve a logger from a plain context (e.g. in unit tests that never
// went through the RED middleware) always get a usable structured logger,
// never a nil pointer.
func TestLoggerFromContext_FallsBackToDefault_WhenNoneStored(t *testing.T) {
	logger := telemetry.LoggerFromContext(context.Background())
	if logger == nil {
		t.Fatal("LoggerFromContext returned nil")
	}
}

// TestContextWithLogger_RoundTrips verifies a logger stored via
// ContextWithLogger is retrievable via LoggerFromContext.
func TestContextWithLogger_RoundTrips(t *testing.T) {
	var buf bytes.Buffer
	want := telemetry.NewLogger("svc", &buf)

	ctx := telemetry.ContextWithLogger(context.Background(), want)
	got := telemetry.LoggerFromContext(ctx)

	got.Info("marker")
	if !strings.Contains(buf.String(), "marker") {
		t.Errorf("LoggerFromContext did not return the stored logger; buf: %s", buf.String())
	}
}
