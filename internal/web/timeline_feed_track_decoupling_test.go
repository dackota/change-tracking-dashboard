// Package web_test (this file): behavioral coverage for decoupling the feed
// (and its click-to-detail panel) from the timeline track's own
// #timeline-root — the change that lets GET /changes (R2) reuse exactly the
// same timeline.js feed rendering the Timeline page uses, on a page that
// intentionally omits the zoomable track, its From/To/Reset-zoom controls,
// and the facet dropdowns ("browse change history without the timeline in
// the way"). Every assertion here follows the same structural-source pattern
// as timeline_feed_rows_test.go: there is no browser/DOM test harness, so
// client-side control flow is verified against the exact source served at
// /static/timeline.js.
package web_test

import (
	"strings"
	"testing"
)

// TestTimelineJS_Init_WiresFeedRegardlessOfTimelineRootPresence verifies
// init() only builds the timeline track, its controls, the facet dropdowns,
// and the header Reset-zoom wiring when #timeline-root is present (unchanged
// Timeline-page behavior), but wires the feed elements and kicks off
// loadBackdrop() unconditionally — so a page with no #timeline-root (the
// Changes page) still gets a live, populated feed.
func TestTimelineJS_Init_WiresFeedRegardlessOfTimelineRootPresence(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "init")

	rootGuardIdx := strings.Index(fn, "if (root) {")
	if rootGuardIdx == -1 {
		t.Fatalf("init() does not guard timeline-track setup behind `if (root) {`:\n%s", fn)
	}
	guardBody, ok := braceDelimitedSpan(fn, rootGuardIdx)
	if !ok {
		t.Fatalf("could not find the matching closing brace for init()'s `if (root)` guard:\n%s", fn)
	}

	for _, want := range []string{"buildControls()", "svgEl(", "attachInteractions()", "buildFacetDropdowns(", "header-reset-zoom"} {
		if !strings.Contains(guardBody, want) {
			t.Errorf("init()'s `if (root)` guard is missing expected track/facet setup %q:\n%s", want, guardBody)
		}
	}

	// The feed's own wiring must never be nested inside the track-only guard.
	for _, mustNotBeGuarded := range []string{"feedEls.list", "feedEls.title", "feedEls.count", "loadBackdrop()"} {
		if strings.Contains(guardBody, mustNotBeGuarded) {
			t.Errorf("init()'s `if (root)` guard wraps %q — the feed would never initialize on a page with no #timeline-root:\n%s", mustNotBeGuarded, guardBody)
		}
	}

	// ...but they must still be present somewhere in init(), unconditionally.
	for _, want := range []string{
		"feedEls.list = document.getElementById('feed-list');",
		"feedEls.title = document.getElementById('feed-title');",
		"feedEls.count = document.getElementById('feed-count');",
		"loadBackdrop();",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("init() is missing expected unconditional feed wiring %q:\n%s", want, fn)
		}
	}
}

// TestTimelineJS_EnsureDetailPanel_FallsBackToFeedPanelWithoutTimelineRoot
// verifies a clicked feed row's detail panel mounts into #timeline-root when
// present (unchanged Timeline-page behavior), or into #feed-panel when it is
// not (the Changes page) — so click-to-detail keeps working on any page that
// renders the feed, not only the Timeline page.
func TestTimelineJS_EnsureDetailPanel_FallsBackToFeedPanelWithoutTimelineRoot(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)

	if !strings.Contains(body, "detailHost = root || document.getElementById('feed-panel');") {
		t.Fatalf("timeline.js does not resolve a detailHost falling back to #feed-panel when #timeline-root is absent")
	}

	fn := extractFunctionBody(t, body, "ensureDetailPanel")
	if !strings.Contains(fn, "detailHost") {
		t.Errorf("ensureDetailPanel does not use detailHost to mount the panel:\n%s", fn)
	}
	if strings.Contains(fn, "root.appendChild") {
		t.Errorf("ensureDetailPanel still appends directly to root instead of detailHost:\n%s", fn)
	}
}
