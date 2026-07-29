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
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// quietRoutesFixture wires a Middleware over newTestMux with the given quiet
// routes, returning the handler and the buffer its logger writes to.
func quietRoutesFixture(t *testing.T, mux http.Handler, quiet ...string) (http.Handler, *bytes.Buffer) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	red, err := telemetry.NewREDMetrics(mp, "http")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	buf := &bytes.Buffer{}
	logger := telemetry.NewLogger("svc", buf)

	return telemetry.Middleware(mux, tp.Tracer("http"), red, logger,
		telemetry.WithQuietRoutes(quiet...)), buf
}

// requestLogLines returns every "http request handled" line in buf, decoded.
func requestLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("log line not JSON: %v (line: %s)", err, line)
		}
		if fields["message"] == "http request handled" {
			out = append(out, fields)
		}
	}
	return out
}

// TestMiddleware_QuietRoute_EmitsNoRequestLog verifies R-138: a route listed
// as quiet (the Kubernetes probe endpoint) emits no "http request handled"
// line. Probes hit /healthz 10x/minute; at that rate the line drowns out
// every real log entry.
func TestMiddleware_QuietRoute_EmitsNoRequestLog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler, buf := quietRoutesFixture(t, mux, "GET /healthz")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if lines := requestLogLines(t, buf); len(lines) != 0 {
		t.Errorf("got %d request log lines for a quiet route, want 0: %v", len(lines), lines)
	}
}

// TestMiddleware_QuietRoute_StillLogsServerError verifies the suppression is
// scoped to routine success: a quiet route that 5xxes is still logged, so
// silencing probe noise never silences a real failure.
func TestMiddleware_QuietRoute_StillLogsServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	handler, buf := quietRoutesFixture(t, mux, "GET /healthz")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := requestLogLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d request log lines for a failing quiet route, want 1", len(lines))
	}
	if lines[0]["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", lines[0]["level"])
	}
}

// TestMiddleware_NonQuietRoute_StillLogged verifies quieting one route leaves
// every other route's request log untouched.
func TestMiddleware_NonQuietRoute_StillLogged(t *testing.T) {
	handler, buf := quietRoutesFixture(t, newTestMux(), "GET /healthz")

	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := requestLogLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d request log lines for a non-quiet route, want 1", len(lines))
	}
	if lines[0]["route"] != "GET /trackers" {
		t.Errorf("route = %v, want GET /trackers", lines[0]["route"])
	}
}

// TestMiddleware_NoQuietRoutes_LogsEverything verifies the option is
// opt-in: a Middleware built without WithQuietRoutes logs exactly as before.
func TestMiddleware_NoQuietRoutes_LogsEverything(t *testing.T) {
	handler, buf := quietRoutesFixture(t, newTestMux())

	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if lines := requestLogLines(t, buf); len(lines) != 1 {
		t.Fatalf("got %d request log lines, want 1", len(lines))
	}
}

// TestMiddleware_QuietRoute_StillRecordsREDMetrics verifies suppression is
// log-only: RED metrics are bounded-cardinality and cheap, and probe
// throughput/latency is exactly the signal an operator wants retained.
func TestMiddleware_QuietRoute_StillRecordsREDMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	red, err := telemetry.NewREDMetrics(mp, "http")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := telemetry.Middleware(mux, tp.Tracer("http"), red,
		telemetry.NewLogger("svc", &bytes.Buffer{}),
		telemetry.WithQuietRoutes("GET /healthz"))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := metricAttr(rm, "operation.requests", "operation"); len(got) != 1 || got[0] != "GET /healthz" {
		t.Errorf("operation.requests attrs = %v, want [GET /healthz]", got)
	}
}
