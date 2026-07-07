// Package web_test (this file): the responsive-layout slice (Theme C —
// Reach, R15-R18 of the shell-operability-polish PRD). It has no browser
// harness (see regression_harness_test.go's doc comment for why this
// project's CI never depends on one), so these tests assert the responsive
// CSS/markup contract the same way the rest of internal/web tests assert any
// other rendered fragment: httptest against the real handlers, checking the
// exact breakpoint rules and truncation affordances are present in the served
// HTML. Trackers is used as the "any page" probe for shell-wide behavior
// (R15, R18's header/body backstop) since it's the cheapest handler to
// construct; page-specific behavior (R16's KPI grid) is asserted against the
// page that actually owns that markup.
package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/config"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// TestSharedShell_CollapsesSidebarBelowNarrowBreakpoint verifies R15: the
// shared shell CSS (rendered on every page) carries an ~860px breakpoint
// that turns the sidebar from a fixed-width column into a full-width
// top bar, rather than staying a fixed-width column at narrow viewports.
func TestSharedShell_CollapsesSidebarBelowNarrowBreakpoint(t *testing.T) {
	t.Parallel()

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: &config.Config{}}, pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "@media (max-width: 860px)") {
		t.Errorf("body missing the ~860px narrow-shell breakpoint; got:\n%s", body)
	}
	if !strings.Contains(body, ".sidebar { flex: 0 0 auto; width: 100%;") {
		t.Errorf("body missing the collapsed-sidebar (full-width top bar) rule; got:\n%s", body)
	}
	if !strings.Contains(body, ".app { flex-direction: column; }") {
		t.Errorf("body missing the column-stacked .app rule for the narrow breakpoint; got:\n%s", body)
	}
}

// TestTimelineHandler_KPIRowReflowsAtNarrowBreakpoints verifies R16: the
// Timeline page's KPI tile row (the only page that renders one) reflows its
// column count in two steps as the viewport narrows — never staying a fixed
// wide grid that would clip tiles.
func TestTimelineHandler_KPIRowReflowsAtNarrowBreakpoints(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t), pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		".kpis { grid-template-columns: repeat(2, 1fr); }",
		".kpis { grid-template-columns: 1fr; }",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing KPI reflow rule %q; got:\n%s", want, body)
		}
	}
}

// TestTrackersHandler_LongRepoNameTruncatesWithTitleTooltip verifies R17: a
// tracker row's repository name renders with a native title tooltip carrying
// the full value, and the column is CSS-truncated (ellipsis, no wrap) rather
// than left to wrap into a multi-line stack.
func TestTrackersHandler_LongRepoNameTruncatesWithTitleTooltip(t *testing.T) {
	t.Parallel()

	const longRepo = "github.com/some-very-long-organization-name/an-extremely-long-repository-name"
	cfg := &config.Config{
		TrackerConfigs: []config.ResolvedTracker{{Repo: longRepo, PollIntervalSeconds: 60}},
	}

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: cfg}, pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `<td class="trackers-repo" title="`+longRepo+`">`) {
		t.Errorf("body missing title-tooltip affordance on the repo cell; got:\n%s", body)
	}
	if !strings.Contains(body, ".trackers-repo { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;") {
		t.Errorf("body missing truncation CSS for .trackers-repo; got:\n%s", body)
	}
}

// TestRepositoriesHandler_LongRepoNameTruncatesWithTitleTooltip verifies R17
// on the Repositories view: a repository row's name renders with a native
// title tooltip carrying the full value, and the column is CSS-truncated
// rather than wrapping into a multi-line stack.
func TestRepositoriesHandler_LongRepoNameTruncatesWithTitleTooltip(t *testing.T) {
	t.Parallel()

	const longRepo = "github.com/some-very-long-organization-name/an-extremely-long-repository-name"
	s := newTestStore(t)
	seedChange(t, s, changeSpec{Repo: longRepo, FilePath: "values.yaml", CommitSha: "sha-1", Age: 0})

	h := web.NewRepositoriesHandler(s, pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/repositories", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `<td class="repositories-repo" title="`+longRepo+`">`) {
		t.Errorf("body missing title-tooltip affordance on the repo cell; got:\n%s", body)
	}
	if !strings.Contains(body, ".repositories-repo { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;") {
		t.Errorf("body missing truncation CSS for .repositories-repo; got:\n%s", body)
	}
}

// TestChangesHandler_FeedAndDetailRepoNamesCarryTruncationCSS verifies R17
// for the two remaining repo-name render sites shared by the Timeline and
// Changes pages (via feedStyles/detailStyles, checked here through the
// Changes page since it's the cheapest handler that includes both):
//   - .feed-repo, the feed table's repo cell (timeline.js sets its title
//     attribute per row at render time; the CSS truncation must ship
//     server-side regardless of which row data ends up in it).
//   - .changeset-detail-repo, the expanded detail panel's repo name (already
//     carries a title attribute — see changeset_detail_render.go).
func TestChangesHandler_FeedAndDetailRepoNamesCarryTruncationCSS(t *testing.T) {
	t.Parallel()

	h := web.NewChangesHandler(pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/changes", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		".feed-repo { font-weight: 600; color: var(--oc-ink); display: inline-block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;",
		".changeset-detail-repo { font-weight: 700; display: inline-block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing truncation CSS %q; got:\n%s", want, body)
		}
	}
}

// TestTableViews_WrapTableInHorizontalScrollContainer verifies R18: every
// page that renders a data table wraps it in the shared .table-scroll
// container (defined once in shellStyles), so a table too wide for the
// viewport scrolls locally instead of forcing the whole page to overflow
// horizontally.
func TestTableViews_WrapTableInHorizontalScrollContainer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		path       string
		tableClass string
		newHandler func(t *testing.T) http.Handler
	}{
		{
			name:       "trackers",
			path:       "/trackers",
			tableClass: "trackers-table",
			newHandler: func(t *testing.T) http.Handler {
				return web.NewTrackersHandler(fakeConfigSnapshot{cfg: &config.Config{}}, pollstatus.New())
			},
		},
		{
			name:       "repositories",
			path:       "/repositories",
			tableClass: "repositories-table",
			newHandler: func(t *testing.T) http.Handler {
				return web.NewRepositoriesHandler(newTestStore(t), pollstatus.New())
			},
		},
		{
			name:       "changes",
			path:       "/changes",
			tableClass: "feed-table",
			newHandler: func(t *testing.T) http.Handler {
				return web.NewChangesHandler(pollstatus.New())
			},
		},
		{
			name:       "timeline",
			path:       "/",
			tableClass: "feed-table",
			newHandler: func(t *testing.T) http.Handler {
				return web.NewTimelineHandler(newTestStore(t), pollstatus.New())
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			tc.newHandler(t).ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
			}

			body := rr.Body.String()
			if !strings.Contains(body, `<div class="table-scroll"><table class="`+tc.tableClass+`">`) {
				t.Errorf("table %q not wrapped in .table-scroll; got:\n%s", tc.tableClass, body)
			}
		})
	}
}

// TestSharedShell_PollChipWrapsInsteadOfOverflowingAtMobileWidth verifies R18
// on a real overflow source: the header's aggregate poll-status chip
// (rendered on every page) concatenates "last poll · next poll · error"
// text with white-space: nowrap, which alone is wide enough to overflow a
// ~375px viewport even though the header row itself wraps. The narrow
// breakpoint must let the chip's own text wrap rather than force a
// horizontal scrollbar on the page.
func TestSharedShell_PollChipWrapsInsteadOfOverflowingAtMobileWidth(t *testing.T) {
	t.Parallel()

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: &config.Config{}}, pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, ".poll-chip { white-space: normal; }") {
		t.Errorf("body missing the narrow-width poll-chip wrap override; got:\n%s", body)
	}
}

// TestSharedShell_PollChipWrapOverrideAppliesAtTabletBreakpoint is a
// fast-follow regression test for a HIGH finding on the responsive-layout
// slice's acceptance gate: the sidebar already collapses into a top bar at
// the 860px breakpoint (R15), but the poll-chip's wrap override previously
// only lived in the 600px media query, leaving a ~620-720px tablet band
// where the chip's nowrap-by-default text still overflows horizontally even
// though the sidebar has already collapsed and nothing else in the layout
// overflows there. The chip's error state is the worst case — it appends a
// "· N tracker(s) failing" suffix — so this test renders the header with two
// failing trackers and asserts the wrap override lives inside the 860px
// media query block specifically, not only the 600px one.
func TestSharedShell_PollChipWrapOverrideAppliesAtTabletBreakpoint(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()
	now := time.Now()
	reg.Record(domain.Tracker{Repo: "repo-a", FileGlob: "Chart.yaml", Field: "version", PollIntervalSeconds: 60}, now, errors.New("network unreachable"))
	reg.Record(domain.Tracker{Repo: "repo-b", FileGlob: "Chart.yaml", Field: "version", PollIntervalSeconds: 60}, now, errors.New("timeout"))

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: &config.Config{}}, reg)
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "2 trackers failing") {
		t.Fatalf("body missing the error-state chip's failure-count text; got:\n%s", body)
	}

	block := mediaQueryBlock(t, body, "@media (max-width: 860px)")
	if !strings.Contains(block, ".poll-chip { white-space: normal; }") {
		t.Errorf("860px media query block missing the poll-chip wrap override; block:\n%s", block)
	}
}

// mediaQueryBlock returns the brace-delimited body of the first CSS media
// query in css whose header is exactly query (e.g. "@media (max-width:
// 860px)"), matching braces by depth so a nested rule's own braces don't
// truncate the block early. It lets a test assert a declaration lives
// inside one specific breakpoint rather than merely appearing anywhere in
// the whole stylesheet.
func mediaQueryBlock(t *testing.T, css, query string) string {
	t.Helper()

	start := strings.Index(css, query)
	if start == -1 {
		t.Fatalf("media query %q not found in CSS:\n%s", query, css)
	}
	openRel := strings.Index(css[start:], "{")
	if openRel == -1 {
		t.Fatalf("media query %q has no opening brace in CSS:\n%s", query, css)
	}
	open := start + openRel

	depth := 0
	for i := open; i < len(css); i++ {
		switch css[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[open+1 : i]
			}
		}
	}
	t.Fatalf("media query %q never closes in CSS:\n%s", query, css)
	return ""
}
