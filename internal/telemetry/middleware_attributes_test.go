package telemetry_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// spanAttributes runs one request through the middleware and returns the
// attributes recorded on its span, keyed by attribute name.
func spanAttributes(t *testing.T, req *http.Request, opts ...telemetry.MiddlewareOption) map[attribute.Key]attribute.Value {
	t.Helper()

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
	red, err := telemetry.NewREDMetrics(mp, "http")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	handler := telemetry.Middleware(newTestMux(), tp.Tracer("http"), red, telemetry.NewLogger("svc", &bytes.Buffer{}), opts...)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}

	attrs := make(map[attribute.Key]attribute.Value, len(spans[0].Attributes))
	for _, kv := range spans[0].Attributes {
		attrs[kv.Key] = kv.Value
	}
	return attrs
}

// TestMiddleware_SetsHTTPAttributes verifies the request span carries the
// dimensions a query can break down by. Without these the span is a bare
// duration and nothing can be grouped, which is what the instrumentation
// previously emitted.
func TestMiddleware_SetsHTTPAttributes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	req.Header.Set("User-Agent", "test-agent/1.0")

	attrs := spanAttributes(t, req)

	want := map[attribute.Key]string{
		"http.request.method": http.MethodGet,
		"http.route":          "GET /trackers",
		"url.path":            "/trackers",
		"user_agent.original": "test-agent/1.0",
	}
	for key, wantValue := range want {
		if got := attrs[key].AsString(); got != wantValue {
			t.Errorf("span attribute %s = %q, want %q", key, got, wantValue)
		}
	}
	if got := attrs["http.response.status_code"].AsInt64(); got != http.StatusOK {
		t.Errorf("span attribute http.response.status_code = %d, want %d", got, http.StatusOK)
	}
}

// TestMiddleware_VisitorID_OffByDefault verifies that visitor identity is
// opt-in, and that neither an identifier nor the client's address reaches a
// span until the deployment configures a salt.
func TestMiddleware_VisitorID_OffByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	req.RemoteAddr = "203.0.113.7:54321"

	attrs := spanAttributes(t, req)

	if _, ok := attrs["visitor.id"]; ok {
		t.Error("visitor.id was set without a configured salt")
	}
	if _, ok := attrs["client.address"]; ok {
		t.Error("client.address was exported; the raw address is deliberately not put on spans")
	}
}

// TestMiddleware_VisitorID_SetWhenConfigured verifies the attribute a
// unique-visitor count reads appears once a salt is configured, and that two
// requests from the same client on the same day carry the same value.
func TestMiddleware_VisitorID_SetWhenConfigured(t *testing.T) {
	identity := telemetry.VisitorIdentity{Salt: "a-salt"}

	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
		req.RemoteAddr = "203.0.113.7:54321"
		req.Header.Set("User-Agent", "test-agent/1.0")
		return req
	}

	first := spanAttributes(t, newReq(), telemetry.WithVisitorIdentity(identity))
	second := spanAttributes(t, newReq(), telemetry.WithVisitorIdentity(identity))

	firstID := first["visitor.id"].AsString()
	if firstID == "" {
		t.Fatal("visitor.id was not set despite a configured salt")
	}
	if secondID := second["visitor.id"].AsString(); secondID != firstID {
		t.Errorf("visitor.id differs between requests from the same client: %q vs %q", firstID, secondID)
	}
}
