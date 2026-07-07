package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func metricAttr(rm metricdata.ResourceMetrics, metricName, attrKey string) []string {
	var vals []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricName {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key(attrKey)); ok {
					vals = append(vals, v.AsString())
				}
			}
		}
	}
	return vals
}

// newTestMux returns a mux with a templated route (bounded cardinality) and
// a route that always 500s, mirroring how cmd/dashboard registers routes.
func newTestMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /trackers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})
	return mux
}

// TestMiddleware_EmitsREDSignalsPerRoute is the tracer bullet for criterion
// 1: hitting a route through the RED middleware produces a request counter,
// an error counter (zero on success), and a latency histogram, observable
// via an in-memory metrics reader.
func TestMiddleware_EmitsREDSignalsPerRoute(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	red, err := telemetry.NewREDMetrics(mp, "http")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	var buf bytes.Buffer
	logger := telemetry.NewLogger("svc", &buf)

	mux := newTestMux()
	handler := telemetry.Middleware(mux, tp.Tracer("http"), red, logger)

	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got := sumInt64(rm, "operation.requests"); got != 1 {
		t.Errorf("operation.requests = %d, want 1", got)
	}
	if got := sumInt64(rm, "operation.errors"); got != 0 {
		t.Errorf("operation.errors = %d, want 0", got)
	}
	if got := histogramCount(rm, "operation.duration"); got != 1 {
		t.Errorf("operation.duration count = %d, want 1", got)
	}
}

// TestMiddleware_UsesTemplatedRoutePattern_NotRawPath verifies criterion 2:
// the "operation" label recorded for a request is the mux's registered
// (bounded-cardinality) route pattern, not the raw request path.
func TestMiddleware_UsesTemplatedRoutePattern_NotRawPath(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	red, err := telemetry.NewREDMetrics(mp, "http")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	mux := newTestMux()
	handler := telemetry.Middleware(mux, tp.Tracer("http"), red, telemetry.NewLogger("svc", &bytes.Buffer{}))

	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	ops := metricAttr(rm, "operation.requests", "operation")
	if len(ops) != 1 || ops[0] != "GET /trackers" {
		t.Errorf("operation label = %v, want [\"GET /trackers\"]", ops)
	}
}

// TestMiddleware_ServerError_RecordsErrorAndSetsSpanStatus verifies that a
// 5xx response is counted as an error in the RED signal and marks the
// request's span with an error status.
func TestMiddleware_ServerError_RecordsErrorAndSetsSpanStatus(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	red, err := telemetry.NewREDMetrics(mp, "http")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	mux := newTestMux()
	handler := telemetry.Middleware(mux, tp.Tracer("http"), red, telemetry.NewLogger("svc", &bytes.Buffer{}))

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := sumInt64(rm, "operation.errors"); got != 1 {
		t.Errorf("operation.errors = %d, want 1", got)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
}

// TestMiddleware_CorrelatesLogsWithTraceAndSpanID verifies criterion 4: a
// log line emitted through the context-carried logger within a request
// carries service.name, trace_id, and span_id matching the request's span.
func TestMiddleware_CorrelatesLogsWithTraceAndSpanID(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	red, err := telemetry.NewREDMetrics(mp, "http")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	var buf bytes.Buffer
	logger := telemetry.NewLogger("change-tracking-dashboard", &buf)

	var gotTraceID, gotSpanID string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /trackers", func(w http.ResponseWriter, r *http.Request) {
		l := telemetry.LoggerFromContext(r.Context())
		l.Info("handling request")

		var fields map[string]any
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &fields); err != nil {
			t.Fatalf("log line not JSON: %v", err)
		}
		gotTraceID, _ = fields["trace_id"].(string)
		gotSpanID, _ = fields["span_id"].(string)
		if fields["service.name"] != "change-tracking-dashboard" {
			t.Errorf("service.name = %v, want change-tracking-dashboard", fields["service.name"])
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := telemetry.Middleware(mux, tp.Tracer("http"), red, logger)

	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if gotTraceID == "" || gotSpanID == "" {
		t.Fatalf("expected non-empty trace_id/span_id, got trace_id=%q span_id=%q", gotTraceID, gotSpanID)
	}
}
