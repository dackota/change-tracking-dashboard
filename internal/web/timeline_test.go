package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// TestTimelineHandler_ReturnsHTMLShell verifies the tracer bullet: GET / on
// the timeline handler returns 200, text/html, and the page references the
// embedded timeline script by a first-party path (never an external CDN URL).
func TestTimelineHandler_ReturnsHTMLShell(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	body := rr.Body.String()
	if body == "" {
		t.Fatal("empty response body — handler panicked or returned nothing")
	}

	if !strings.Contains(body, `/static/timeline.js`) {
		t.Errorf("body missing first-party script reference '/static/timeline.js'; got:\n%s", body)
	}

	// No external CDN URL of any kind should appear anywhere in the page.
	for _, cdnMarker := range []string{"cdn.", "unpkg.com", "jsdelivr", "googleapis.com", "http://", "https://"} {
		if strings.Contains(body, cdnMarker) {
			t.Errorf("body contains a possible external URL marker %q — script must be first-party only", cdnMarker)
		}
	}
}

// TestStaticHandler_ServesEmbeddedTimelineJS verifies the vendored timeline
// script is served first-party via go:embed at GET /static/timeline.js with
// a JavaScript content-type and a non-empty body.
func TestStaticHandler_ServesEmbeddedTimelineJS(t *testing.T) {
	t.Parallel()

	h := web.NewStaticHandler()

	req := httptest.NewRequest(http.MethodGet, "/static/timeline.js", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want a javascript content-type", ct)
	}

	if rr.Body.Len() == 0 {
		t.Error("empty response body — expected the vendored timeline script")
	}
}

// TestTimelineHandler_CSPPermitsOnlySelfScript verifies the CSP header on
// GET / permits script execution only from 'self' (the embedded, first-party
// script) — never an external origin, and never 'unsafe-inline'/
// 'unsafe-eval' — while the other conservative security headers stay intact.
func TestTimelineHandler_CSPPermitsOnlySelfScript(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing Content-Security-Policy header")
	}

	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q, want it to contain script-src 'self'", csp)
	}

	// Isolate the script-src directive specifically: style-src is legitimately
	// allowed 'unsafe-inline' for CSS, but script-src must never be.
	scriptSrc := extractDirective(csp, "script-src")
	if scriptSrc == "" {
		t.Fatalf("CSP = %q, missing script-src directive", csp)
	}
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "cdn", "http://", "https://", "*"} {
		if strings.Contains(scriptSrc, forbidden) {
			t.Errorf("script-src = %q, must not contain forbidden marker %q", scriptSrc, forbidden)
		}
	}

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

// TestStaticHandler_SecurityHeaders verifies the static asset response
// carries the same conservative security headers as the rest of the
// dashboard — the security posture must not regress just because a response
// is a static file.
func TestStaticHandler_SecurityHeaders(t *testing.T) {
	t.Parallel()

	h := web.NewStaticHandler()
	req := httptest.NewRequest(http.MethodGet, "/static/timeline.js", nil)
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
	if csp := rr.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("missing Content-Security-Policy header on static asset response")
	}
}

// TestTimelineHandler_RootPathOnly verifies the timeline handler behaves
// correctly regardless of query string (the page shell itself takes no
// server-side facet/asOf params — those are read client-side by the
// embedded script and sent to /api/changesets) and never 500s.
func TestTimelineHandler_RootPathOnly(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler()
	req := httptest.NewRequest(http.MethodGet, "/?anything=ignored", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Body.Len() == 0 {
		t.Error("empty body — handler panicked")
	}
}

// TestStaticHandler_UnknownAssetReturnsNotFound verifies the static handler
// does not serve arbitrary files (e.g. a path-traversal attempt or an asset
// that was never embedded) — only the assets actually embedded via go:embed
// are reachable.
func TestStaticHandler_UnknownAssetReturnsNotFound(t *testing.T) {
	t.Parallel()

	h := web.NewStaticHandler()

	for _, path := range []string{"/static/does-not-exist.js", "/static/../go.mod", "/static/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if rr.Code == http.StatusOK {
			t.Errorf("path %q: status = 200, want non-200 (only embedded assets should be servable)", path)
		}
	}
}
