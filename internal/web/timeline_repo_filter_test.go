// Package web_test (this file): behavioral coverage for the client-side
// repo-filter wiring (R26/R27) in the served timeline.js. There is no
// browser/DOM test harness in this repo, so — following the structural-
// source pattern established by timeline_feed_rows_test.go and
// timeline_feed_track_decoupling_test.go — client-side control flow is
// verified against the exact source served at /static/timeline.js, the same
// bytes a browser executes.
package web_test

import (
	"strings"
	"testing"
)

// TestTimelineJS_BuildFilterParams_IncludesRepoScopeWhenSet verifies that
// buildFilterParams — the single function that assembles the query params
// sent to /api/changesets — includes a "repo" pair whenever a repo has been
// selected, and that it is guarded so an unselected ("" / "All
// repositories") scope emits no "repo" pair at all (R27's no-op invariant).
func TestTimelineJS_BuildFilterParams_IncludesRepoScopeWhenSet(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFilterParams")

	if !strings.Contains(fn, "repoState") {
		t.Fatalf("buildFilterParams() does not reference repoState — the repo filter selection is never threaded into the query params:\n%s", fn)
	}
	if !strings.Contains(fn, "'repo'") {
		t.Fatalf("buildFilterParams() does not push a 'repo' param pair:\n%s", fn)
	}
	if !strings.Contains(fn, "if (repoState)") {
		t.Fatalf("buildFilterParams() does not guard the repo pair behind a truthy repoState check — an unselected repo scope must emit no param:\n%s", fn)
	}
}

// TestTimelineJS_FetchChangesetsPage_UsesBuildFilterParams verifies that the
// one shared client-side fetch to /api/changesets (fetchChangesetsPage —
// used both by loadBackdrop's initial/filter-reload fetch with an empty
// cursor and by loadMore's pagination fetch with a real cursor) sources its
// query params from buildFilterParams(), so the repo scope reaches this fetch the
// exact same way existing facet filters already do (R27) — there is no
// second, separately-assembled param list to drift out of sync. Neither
// this function nor buildFilterParams ever references "asOf" — the
// backdrop/pagination fetch's no-asOf invariant (R27) is preserved unchanged
// by adding the repo param.
func TestTimelineJS_FetchChangesetsPage_UsesBuildFilterParams(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "fetchChangesetsPage")

	if !strings.Contains(fn, "buildFilterParams()") {
		t.Fatalf("fetchChangesetsPage() does not call buildFilterParams() — it would need its own separate repo-param wiring:\n%s", fn)
	}
	if strings.Contains(fn, "asOf") {
		t.Errorf("fetchChangesetsPage() references asOf — the timeline backdrop/pagination fetch must never send asOf (R27):\n%s", fn)
	}
}

// TestTimelineJS_Init_WiresRepoFilterInsideTimelineRootGuard verifies that
// the repo filter <select> is wired up alongside the other Timeline-page-
// only facet-bar chrome (inside init()'s `if (root) {` guard, so it is never
// looked up on a page without #timeline-root such as /changes), and that
// choosing an option refreshes the backdrop through the same
// onFilterChanged/loadBackdrop path facet changes already use.
func TestTimelineJS_Init_WiresRepoFilterInsideTimelineRootGuard(t *testing.T) {
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

	if !strings.Contains(guardBody, "getElementById('repo-filter')") {
		t.Fatalf("init()'s `if (root)` guard does not look up #repo-filter:\n%s", guardBody)
	}

	changeCallback := extractCallbackAfter(t, guardBody, "addEventListener('change',")
	if !strings.Contains(changeCallback, "repoState") {
		t.Errorf("the repo filter's change handler does not update repoState:\n%s", changeCallback)
	}
	if !strings.Contains(changeCallback, "onFilterChanged()") && !strings.Contains(changeCallback, "loadBackdrop()") {
		t.Errorf("the repo filter's change handler does not refresh the backdrop (expected onFilterChanged() or loadBackdrop()):\n%s", changeCallback)
	}
}
