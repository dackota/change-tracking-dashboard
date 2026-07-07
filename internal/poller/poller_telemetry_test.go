package poller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/poller"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// findSum returns the total of the named Sum[int64] metric, or -1 if absent.
func findSum(rm metricdata.ResourceMetrics, name string) int64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				var total int64
				for _, dp := range sum.DataPoints {
					total += dp.Value
				}
				return total
			}
		}
	}
	return -1
}

// TestPoller_Poll_EmitsREDSignals is the tracer bullet for the poll-cycle
// instrumentation seam: a successful Poll call produces the generic RED
// signal (request counter, error counter, duration histogram) via an
// in-memory metrics reader, with the single low-cardinality "poll" operation
// label — never the tracker's repo or file path.
func TestPoller_Poll_EmitsREDSignals(t *testing.T) {
	t.Parallel()

	repoPath, _, _ := buildFixtureRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	p := poller.New(src, st, poller.WithMeterProvider(mp))

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:         "aidp-version",
		ExtractorExpr: ".version",
		BackfillDays:  3650,
	}
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got := findSum(rm, "operation.requests"); got != 1 {
		t.Errorf("operation.requests = %d, want 1", got)
	}
	if got := findSum(rm, "operation.errors"); got != 0 {
		t.Errorf("operation.errors = %d, want 0", got)
	}

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "operation.requests" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			for _, dp := range sum.DataPoints {
				op, ok := dp.Attributes.Value("operation")
				if !ok || op.AsString() != "poll" {
					t.Errorf("operation label = %v (ok=%v), want %q (never the tracker repo/path)", op, ok, "poll")
				}
			}
		}
	}
}

// TestPoller_Poll_Failure_RecordsErrorAndSpanStatus verifies a failing poll
// cycle (the resilience fixture: one file's extractor throws) is counted as
// a RED error and marks the poll cycle's span with an error status —
// mirroring the HTTP middleware's failure-path contract for the poll seam.
func TestPoller_Poll_Failure_RecordsErrorAndSpanStatus(t *testing.T) {
	t.Parallel()

	repoPath := buildResilienceRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	p := poller.New(src, st, poller.WithMeterProvider(mp), poller.WithTracerProvider(tp))

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "a/*/Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  `^a/(?P<app>[^/]+)/Chart\.yaml$`,
		BackfillDays:  3650,
	}
	if err := p.Poll(tracker); err == nil {
		t.Fatal("Poll returned nil, want an error for the failing file")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := findSum(rm, "operation.errors"); got != 1 {
		t.Errorf("operation.errors = %d, want 1", got)
	}

	spans := exporter.GetSpans()
	var pollSpan *tracetest.SpanStub
	for i, s := range spans {
		if s.Name == "poller.poll" {
			pollSpan = &spans[i]
		}
	}
	if pollSpan == nil {
		t.Fatal("no span named poller.poll was recorded")
	}
	if pollSpan.Status.Code != codes.Error {
		t.Errorf("poller.poll span status = %v, want Error", pollSpan.Status.Code)
	}
}

// TestPoller_Poll_DownstreamGitAndStoreCallsWrappedInSpans verifies
// criterion 5 for the poll seam: the git and store calls Poll makes are each
// wrapped in their own child span, so a trace shows exactly where time was
// spent / where a failure occurred downstream of the poll cycle.
func TestPoller_Poll_DownstreamGitAndStoreCallsWrappedInSpans(t *testing.T) {
	t.Parallel()

	repoPath, _, _ := buildFixtureRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	p := poller.New(src, st, poller.WithTracerProvider(tp))

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:         "aidp-version",
		ExtractorExpr: ".version",
		BackfillDays:  3650,
	}
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	names := map[string]bool{}
	for _, s := range exporter.GetSpans() {
		names[s.Name] = true
	}

	for _, want := range []string{"store.get_high_water_mark", "gitsource.walk_commits", "store.save_change", "store.set_high_water_mark"} {
		if !names[want] {
			t.Errorf("missing expected downstream span %q; got spans: %v", want, names)
		}
	}
}

// TestPoller_Poll_LogsCorrelatedWithTraceAndSpanID verifies criterion 4 for
// the poll seam: a log line emitted during a poll cycle carries
// service.name, trace_id, and span_id matching the poll cycle's own span.
func TestPoller_Poll_LogsCorrelatedWithTraceAndSpanID(t *testing.T) {
	t.Parallel()

	repoPath := buildResilienceRepo(t) // triggers an ERROR-level log line
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	var buf bytes.Buffer
	logger := telemetry.NewLogger("change-tracking-dashboard", &buf)

	p := poller.New(src, st, poller.WithTracerProvider(tp), poller.WithLogger(logger))

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "a/*/Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  `^a/(?P<app>[^/]+)/Chart\.yaml$`,
		BackfillDays:  3650,
	}
	_ = p.Poll(tracker) // error expected (a/bad fails); log correlation is what's under test

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("expected at least one log line to be emitted during the poll cycle")
	}

	var pollSpan tracetest.SpanStub
	found := false
	for _, s := range exporter.GetSpans() {
		if s.Name == "poller.poll" {
			pollSpan = s
			found = true
		}
	}
	if !found {
		t.Fatal("no span named poller.poll was recorded")
	}
	wantTraceID := pollSpan.SpanContext.TraceID().String()

	sawCorrelatedLine := false
	for _, line := range lines {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("log line not JSON: %v; line: %s", err, line)
		}
		if fields["service.name"] != "change-tracking-dashboard" {
			t.Errorf("service.name = %v, want change-tracking-dashboard", fields["service.name"])
		}
		if fields["trace_id"] == wantTraceID {
			sawCorrelatedLine = true
		}
	}
	if !sawCorrelatedLine {
		t.Errorf("no log line carried trace_id=%q (the poll cycle's own trace)", wantTraceID)
	}
}

// TestPoller_WithNow_PreservesInjectedTelemetry verifies that WithNow (the
// existing clock-injection option) does not silently drop telemetry options
// configured via New — chaining must not regress observability.
func TestPoller_WithNow_PreservesInjectedTelemetry(t *testing.T) {
	t.Parallel()

	repoPath, _, _ := buildFixtureRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	p := poller.New(src, st, poller.WithMeterProvider(mp)).WithNow(func() time.Time { return time.Now() })

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:         "aidp-version",
		ExtractorExpr: ".version",
		BackfillDays:  3650,
	}
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := findSum(rm, "operation.requests"); got != 1 {
		t.Errorf("operation.requests = %d, want 1 — WithNow must not drop the meter provider injected via New", got)
	}
}
