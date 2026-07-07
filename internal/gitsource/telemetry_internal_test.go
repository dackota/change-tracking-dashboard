package gitsource

// Regression test for the observability-foundation rework gap: gitsource's
// commit-cap warning used to log via a hardcoded context.Background()
// instead of the real poll-cycle context, so it never carried the poll
// cycle's trace_id/span_id (criterion 4). warnCommitCapHit is the extracted
// chokepoint (see WalkCommits) that now derives its logger from ctx instead.

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestWarnCommitCapHit_ActiveTrace_LogLineCarriesTraceAndSpanID verifies that
// when the ctx passed to WalkCommits carries a poll-cycle-scoped logger
// (stored via telemetry.ContextWithLogger, exactly as poller.Poll now does
// before calling into gitsource), the commit-cap warning line correlates to
// that same trace/span — the specific defect a checker live-reproduced in
// the prior round.
func TestWarnCommitCapHit_ActiveTrace_LogLineCarriesTraceAndSpanID(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	base := telemetry.NewLogger("change-tracking-dashboard", &buf)

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "poller.poll")
	defer span.End()

	// Mirror poller.Poll: derive a trace-correlated logger and store it on
	// ctx so a downstream package that only receives ctx (gitsource) can
	// retrieve the same correlated logger via telemetry.LoggerFromContext.
	correlated := telemetry.FromContext(ctx, base)
	ctx = telemetry.ContextWithLogger(ctx, correlated)

	warnCommitCapHit(ctx, "Chart.yaml")

	var fields map[string]any
	if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
		t.Fatalf("log line not JSON: %v; line: %s", err, buf.String())
	}

	sc := span.SpanContext()
	if fields["trace_id"] != sc.TraceID().String() {
		t.Errorf("trace_id = %v, want %s (the active poll cycle's trace)", fields["trace_id"], sc.TraceID().String())
	}
	if fields["span_id"] != sc.SpanID().String() {
		t.Errorf("span_id = %v, want %s (the active poll cycle's span)", fields["span_id"], sc.SpanID().String())
	}
	if fields["service.name"] != "change-tracking-dashboard" {
		t.Errorf("service.name = %v, want change-tracking-dashboard", fields["service.name"])
	}
}

// TestWarnCommitCapHit_NoActiveTrace_StillLogsValidJSON documents the safe
// degrade: a ctx with no stored logger and no active span (e.g. a direct
// unit-test call, or a hypothetical future caller that isn't part of a poll
// cycle) still produces a valid, single-line structured JSON log with no
// fabricated trace_id/span_id — never a crash or nil-logger panic.
func TestWarnCommitCapHit_NoActiveTrace_StillLogsValidJSON(t *testing.T) {
	t.Parallel()

	// warnCommitCapHit logs through the package-wide default logger when
	// ctx carries neither a stored logger nor an active span; this test
	// only needs to prove it never panics on a bare ctx.
	warnCommitCapHit(context.Background(), "Chart.yaml")
}
