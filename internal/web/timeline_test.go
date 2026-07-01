package web_test

import (
	"fmt"
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

	h := web.NewTimelineHandler(newTestStore(t))

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

// TestTimelineHandler_RendersOneControlPerFacetValue verifies that the shell
// renders one tri-state control per known facet value — sourced from
// store.FacetOptions() — carrying data-facet/data-value attributes so
// timeline.js can wire click-to-cycle behavior without any server-side
// template logic beyond rendering the known set.
func TestTimelineHandler_RendersOneControlPerFacetValue(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedChangeWithFacets(t, st, "commit-a", map[string]string{"env": "dev", "tier": "sbx"})
	seedChangeWithFacets(t, st, "commit-b", map[string]string{"env": "prod"})

	h := web.NewTimelineHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []struct{ facet, value string }{
		{"env", "dev"},
		{"env", "prod"},
		{"tier", "sbx"},
	} {
		wantAttr := fmt.Sprintf(`data-facet="%s" data-value="%s"`, want.facet, want.value)
		if !strings.Contains(body, wantAttr) {
			t.Errorf("body missing control for facet=%s value=%s (want attr %q); got:\n%s", want.facet, want.value, wantAttr, body)
		}
	}
}

// TestTimelineHandler_EmptyStore_RendersNoFacetControlsWithoutError verifies
// that an empty store (no Changes, so no known facets) still renders the
// shell successfully with no facet controls — an edge case that must not
// panic or 500.
func TestTimelineHandler_EmptyStore_RendersNoFacetControlsWithoutError(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `data-facet`) {
		t.Errorf("expected no facet controls for an empty store; got:\n%s", rr.Body.String())
	}
}

// TestTimelineHandler_FacetValueIsHTMLEscaped verifies that a facet value
// containing HTML-significant characters is rendered escaped (via
// html/template auto-escaping), never string-concatenated raw into the page.
func TestTimelineHandler_FacetValueIsHTMLEscaped(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	const maliciousValue = `"><script>alert(1)</script>`
	seedChangeWithFacets(t, st, "commit-xss", map[string]string{"env": maliciousValue})

	h := web.NewTimelineHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("facet value rendered unescaped — raw <script> tag present in body:\n%s", body)
	}
}

// TestTimelineHandler_FacetControlsHaveNoInlineEventHandlers verifies the
// rendered facet controls carry no inline event-handler attribute (e.g.
// onclick=...) — click-to-cycle behavior must be wired entirely from the
// external timeline.js script, matching the CSP's script-src 'self' with no
// 'unsafe-inline' for scripts.
func TestTimelineHandler_FacetControlsHaveNoInlineEventHandlers(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedChangeWithFacets(t, st, "commit-a", map[string]string{"env": "dev"})

	h := web.NewTimelineHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, forbidden := range []string{"onclick=", "onchange=", "javascript:"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("body contains inline event-handler marker %q — behavior must be wired from timeline.js only", forbidden)
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

	h := web.NewTimelineHandler(newTestStore(t))
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

	h := web.NewTimelineHandler(newTestStore(t))
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
