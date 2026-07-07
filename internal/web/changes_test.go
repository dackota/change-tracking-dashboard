// Package web_test (this file): behavioral coverage for the GET /changes
// page (R2) — a full-page Changes view of the changeset feed, rendered from
// the same shared shell every route uses (R6), reusing the exact feed
// rendering (thead/tbody markup plus the embedded timeline.js script) the
// Timeline page already ships rather than duplicating it.
package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// TestChangesHandler_ReturnsHTMLShellWithFeedTable verifies the tracer
// bullet: GET /changes returns 200, text/html, and renders the shared shell
// around the same feed-table markup (thead columns, <tbody id="feed-list">)
// timeline.js's feed-rendering functions already wire up, loaded via the
// same first-party script reference — never a reimplementation.
func TestChangesHandler_ReturnsHTMLShellWithFeedTable(t *testing.T) {
	t.Parallel()

	h := web.NewChangesHandler()
	req := httptest.NewRequest(http.MethodGet, "/changes", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	body := rr.Body.String()
	for _, want := range []string{
		`<h1>Changes</h1>`,
		`<tbody id="feed-list">`,
		`<th>When</th><th>Repository</th><th>Commit</th><th>Author</th><th>Changes</th>`,
		`/static/timeline.js`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; got:\n%s", want, body)
		}
	}
}

// TestChangesHandler_SidebarNav_ChangesActiveOthersUnaffected verifies R1
// and R6: on GET /changes, the Changes nav entry is the active link, and
// Timeline, Repositories, and Trackers are all links but not active — the
// same shared shell every route builds.
func TestChangesHandler_SidebarNav_ChangesActiveOthersUnaffected(t *testing.T) {
	t.Parallel()

	h := web.NewChangesHandler()
	req := httptest.NewRequest(http.MethodGet, "/changes", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `<a class="nav-item nav-item-active" data-nav="changes" href="/changes" aria-current="page">Changes</a>`) {
		t.Errorf("Changes nav entry not rendered as an active link; got:\n%s", body)
	}
	if !strings.Contains(body, `<a class="nav-item" data-nav="timeline" href="/">Timeline</a>`) {
		t.Errorf("Timeline nav entry not rendered as an (inactive) link; got:\n%s", body)
	}
	if !strings.Contains(body, `<a class="nav-item" data-nav="trackers" href="/trackers">Trackers</a>`) {
		t.Errorf("Trackers nav entry not rendered as an (inactive) link; got:\n%s", body)
	}
	if !strings.Contains(body, `<a class="nav-item" data-nav="repositories" href="/repositories">Repositories</a>`) {
		t.Errorf("Repositories nav entry not rendered as an (inactive) link; got:\n%s", body)
	}
}

// TestChangesHandler_Header_ShowsTitleAndSubtitle verifies R6: the page
// header renders the Changes title and a subtitle, via the same shared
// header shell every route builds.
func TestChangesHandler_Header_ShowsTitleAndSubtitle(t *testing.T) {
	t.Parallel()

	h := web.NewChangesHandler()
	req := httptest.NewRequest(http.MethodGet, "/changes", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "<h1>Changes</h1>") {
		t.Errorf("body missing header title; got:\n%s", body)
	}
	if !strings.Contains(body, "page-subtitle") {
		t.Errorf("body missing header subtitle element; got:\n%s", body)
	}
}

// TestChangesHandler_SecurityHeadersPresent verifies R7: the Changes page
// carries the same conservative security headers as every other route.
func TestChangesHandler_SecurityHeadersPresent(t *testing.T) {
	t.Parallel()

	h := web.NewChangesHandler()
	req := httptest.NewRequest(http.MethodGet, "/changes", nil)
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
		t.Error("missing Content-Security-Policy header on Changes page response")
	}
}

// TestChangesHandler_OmitsTimelineTrackButKeepsFeedPanelForDetailMount
// verifies the user story this page exists for ("browse change history
// without the timeline in the way"): no #timeline-root zoomable track, its
// From/To/Reset-zoom controls, or the facet dropdowns render on this page —
// but #feed-panel (the detail-panel mount timeline.js falls back to when
// #timeline-root is absent — see ensureDetailPanel/detailHost) is present,
// so a feed row's click-to-detail still works on this page.
func TestChangesHandler_OmitsTimelineTrackButKeepsFeedPanelForDetailMount(t *testing.T) {
	t.Parallel()

	h := web.NewChangesHandler()
	req := httptest.NewRequest(http.MethodGet, "/changes", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()
	for _, forbidden := range []string{`id="timeline-root"`, `id="facet-controls"`, `id="facet-chips"`, `id="header-reset-zoom"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("body unexpectedly contains %q — the Changes page must omit the timeline track/facet chrome; got:\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, `id="feed-panel"`) {
		t.Errorf("body missing #feed-panel — timeline.js's detail-panel mount fallback has nowhere to attach; got:\n%s", body)
	}
}
