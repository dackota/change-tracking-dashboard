package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// TestHealthzHandler_ReturnsOK verifies R13: GET /healthz always returns 200
// with no dependency checks — a liveness response suitable for a Kubernetes
// probe.
func TestHealthzHandler_ReturnsOK(t *testing.T) {
	t.Parallel()

	h := web.NewHealthzHandler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestHealthzHandler_SecurityHeadersPresent verifies the liveness route
// carries the same conservative security headers as every other route.
func TestHealthzHandler_SecurityHeadersPresent(t *testing.T) {
	t.Parallel()

	h := web.NewHealthzHandler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}
