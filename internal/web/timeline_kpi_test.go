package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// TestTimelineHandler_KPIStoreError_RendersZeroedMetrics is the R21
// invariant: a store-read error on the bounded KPI Changeset query must never
// fail the page. It must log server-side (not asserted here — captured by
// inspection, not stdout capture, per the package's existing degrade tests)
// and render the shell anyway with every KPI tile zeroed, exactly like the
// existing FacetOptions degrade in the same handler. This is written and
// must go RED before any KPI happy-path behavior is wired up.
func TestTimelineHandler_KPIStoreError_RendersZeroedMetrics(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	h := web.NewTimelineHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a KPI store failure must degrade, not fail the page); body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		`data-kpi="changes" data-value="0"`,
		`data-kpi="repositories" data-value="0"`,
		`data-kpi="chart-changes" data-value="0"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing zeroed KPI tile %q on store error; got:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `data-changesets="0"`) {
		t.Errorf("body missing zeroed Changeset count on store error; got:\n%s", body)
	}
	if !strings.Contains(body, `data-kpi="last-change"`) {
		t.Errorf("body missing last-change KPI tile on store error; got:\n%s", body)
	}
	if !strings.Contains(body, "No changes yet") {
		t.Errorf("body missing sensible empty last-change label on store error; got:\n%s", body)
	}
}

// TestTimelineHandler_EmptyStore_KPITilesZeroedWithSensibleLastChange
// verifies the "no data yet" case (as opposed to a store error): an empty,
// healthy store still renders every KPI tile zeroed and a sensible empty
// value for "last change" — never a store error, never a nonsensical
// timestamp.
func TestTimelineHandler_EmptyStore_KPITilesZeroedWithSensibleLastChange(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		`data-kpi="changes" data-value="0"`,
		`data-changesets="0"`,
		`data-kpi="repositories" data-value="0"`,
		`data-kpi="chart-changes" data-value="0"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing zeroed KPI tile %q on empty store; got:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "No changes yet") {
		t.Errorf("body missing sensible empty last-change label on empty store; got:\n%s", body)
	}
}

// TestTimelineHandler_KPITiles_ReflectSeededChangesetMetrics verifies the
// handler's call site passes the right Changeset set into dashboardStats and
// maps the resulting Metrics into the view: total Changes, distinct
// repository count, and Chart-kind Change count — the numbers a Changeset
// set spanning two repos and both Chart.yaml and values.yaml sources
// actually produces (R4, R5, R7).
func TestTimelineHandler_KPITiles_ReflectSeededChangesetMetrics(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	// Commit 1 in repo-a: two Changes (one Chart.yaml, one values.yaml).
	seedChange(t, st, changeSpec{Repo: "repo-a", FilePath: "Chart.yaml", CommitSha: "c1"})
	seedChange(t, st, changeSpec{Repo: "repo-a", FilePath: "values.yaml", CommitSha: "c1"})
	// Commit 2 in repo-b: one Change (Chart.yaml).
	seedChange(t, st, changeSpec{Repo: "repo-b", FilePath: "Chart.yaml", CommitSha: "c2"})

	h := web.NewTimelineHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		`data-kpi="changes" data-value="3"`,       // 3 Changes total
		`data-changesets="2"`,                     // 2 Changesets (commits c1, c2)
		`data-kpi="repositories" data-value="2"`,  // repo-a, repo-b
		`data-kpi="chart-changes" data-value="2"`, // Chart.yaml x2
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing KPI tile %q; got:\n%s", want, body)
		}
	}
}

// TestTimelineHandler_LastChangeKPI_ShowsRelativeAndAbsoluteTimestamp
// verifies R6: the "last change" tile carries both a relative phrase and the
// precise absolute timestamp of the most recent Changeset's commit.
func TestTimelineHandler_LastChangeKPI_ShowsRelativeAndAbsoluteTimestamp(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	recentAge := 2 * time.Hour
	seedChange(t, st, changeSpec{Repo: "repo-a", FilePath: "values.yaml", CommitSha: "recent", Age: recentAge})
	seedChange(t, st, changeSpec{Repo: "repo-a", FilePath: "values.yaml", CommitSha: "older", Age: 30 * 24 * time.Hour})

	h := web.NewTimelineHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	wantAbsolute := time.Now().Add(-recentAge).UTC().Format("Jan 2, 15:04")
	if !strings.Contains(body, `data-absolute="`+wantAbsolute+`"`) {
		t.Errorf("body missing absolute last-change timestamp %q; got:\n%s", wantAbsolute, body)
	}
	if !strings.Contains(body, "hour") {
		t.Errorf("body missing a relative last-change phrase mentioning hours; got:\n%s", body)
	}
}

// TestTimelineHandler_SidebarNav_RegisteredRoutesAreLinksAndCurrentRouteIsActive
// verifies R1 (superseding this test's earlier "every nav entry is an inert
// placeholder" contract now that Timeline and Trackers are real routes):
// Timeline and Trackers render as real <a> links (their routes are
// registered), Timeline is marked active on GET /, Trackers is a link but
// not active, and Changes/Repositories — not yet routes — render as plain,
// non-interactive elements with no href or onclick, so they can never
// produce a dead link ahead of their own slice landing.
func TestTimelineHandler_SidebarNav_RegisteredRoutesAreLinksAndCurrentRouteIsActive(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, navKey := range []string{"timeline", "changes", "repositories", "trackers"} {
		if !strings.Contains(body, `data-nav="`+navKey+`"`) {
			t.Errorf("body missing sidebar nav entry %q; got:\n%s", navKey, body)
		}
	}

	if !strings.Contains(body, `<a class="nav-item nav-item-active" data-nav="timeline" href="/" aria-current="page">Timeline</a>`) {
		t.Errorf("Timeline nav entry not rendered as an active link; got:\n%s", body)
	}
	if !strings.Contains(body, `<a class="nav-item" data-nav="trackers" href="/trackers">Trackers</a>`) {
		t.Errorf("Trackers nav entry not rendered as an (inactive) link; got:\n%s", body)
	}
	if !strings.Contains(body, `<div class="nav-item" data-nav="changes">Changes</div>`) {
		t.Errorf("Changes nav entry not rendered as an inert placeholder; got:\n%s", body)
	}
	if !strings.Contains(body, `<div class="nav-item" data-nav="repositories">Repositories</div>`) {
		t.Errorf("Repositories nav entry not rendered as an inert placeholder; got:\n%s", body)
	}

	if strings.Contains(body, `data-nav="changes" aria-current`) ||
		strings.Contains(body, `data-nav="repositories" aria-current`) ||
		strings.Contains(body, `data-nav="trackers" aria-current`) {
		t.Errorf("a non-current nav entry was marked active; only Timeline should be on GET /; got:\n%s", body)
	}
}

// TestTimelineHandler_Header_ShowsTitleSubtitleAndResetZoomControl verifies
// R2: the header shows the app/page title and subtitle plus the global
// timeline action (Reset zoom), addressable by a stable id.
func TestTimelineHandler_Header_ShowsTitleSubtitleAndResetZoomControl(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "<h1>Timeline</h1>") {
		t.Errorf("body missing header title; got:\n%s", body)
	}
	if !strings.Contains(body, "page-subtitle") {
		t.Errorf("body missing header subtitle element; got:\n%s", body)
	}
	if !strings.Contains(body, `id="header-reset-zoom"`) {
		t.Errorf("body missing header Reset zoom control; got:\n%s", body)
	}
	if !strings.Contains(body, "Reset zoom") {
		t.Errorf("body missing 'Reset zoom' label; got:\n%s", body)
	}
}

// TestTimelineHandler_FeedContainer_IsTableSkeletonPreservingDataHooks
// verifies the feed panel renders a table skeleton (thead: When, Repository,
// Commit, Author, Changes) while keeping the ids timeline.js wires
// (feed-list, feed-title, feed-count) intact. feed-empty — the pre-feed-table
// slice's standalone sibling-div empty-state placeholder — is gone: the
// feed-table slice renders loading/empty/no-match states as full-width rows
// inside <tbody id="feed-list"> itself (see
// TestTimelineJS_RenderFeed_LoadingAndEmptyStatesRenderAsFullWidthTableRows
// in timeline_feed_rows_test.go), so the skeleton no longer needs a separate
// placeholder element for it.
func TestTimelineHandler_FeedContainer_IsTableSkeletonPreservingDataHooks(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		"<table", "<thead", "<th>When</th>", "<th>Repository</th>",
		"<th>Commit</th>", "<th>Author</th>", "<th>Changes</th>",
		`id="feed-list"`, `id="feed-title"`, `id="feed-count"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing feed-table structural element %q; got:\n%s", want, body)
		}
	}
	if strings.Contains(body, `id="feed-empty"`) {
		t.Errorf("body still contains the retired standalone feed-empty placeholder; empty/loading states now render as in-table rows:\n%s", body)
	}
}

// TestTimelineJS_WiresHeaderResetZoomButton verifies the served timeline.js
// wires the header's Reset zoom button (id="header-reset-zoom") to the same
// resetView behavior as the timeline's own embedded control, so R2's header
// action is a real trigger rather than an inert decoy.
func TestTimelineJS_WiresHeaderResetZoomButton(t *testing.T) {
	t.Parallel()

	h := web.NewStaticHandler()
	req := httptest.NewRequest(http.MethodGet, "/static/timeline.js", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "header-reset-zoom") {
		t.Error("served timeline.js does not reference header-reset-zoom — the header's Reset zoom button has nothing wired to it")
	}
}
