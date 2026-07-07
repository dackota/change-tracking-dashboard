// Package web_test (this file): behavioral coverage for R25 — the changeset
// feed consuming the server's already-returned nextCursor to page through
// results beyond the first fetch ("Load more"). The server side
// (GET /api/changesets already returns nextCursor — see api_changesets.go)
// needs no change; every assertion here targets the client-side consumption
// gap in timeline.js. As with timeline_feed_rows_test.go and
// timeline_feed_track_decoupling_test.go, there is no browser/DOM test
// harness in this repo, so client-side control flow is verified against the
// exact source served at /static/timeline.js. The one piece of pure,
// DOM-free logic this slice introduces (the page-merge transformation) has
// its own dedicated property test run under Node — see
// static/feed-pagination.property.test.js — which this file does not
// duplicate.
package web_test

import (
	"strings"
	"testing"
)

// TestTimelineJS_FetchChangesetsPage_OmitsCursorParamOnFirstPage verifies
// the first-page fetch never appends an empty cursor param — cursor is only
// ever sent once a prior page actually returned one.
func TestTimelineJS_FetchChangesetsPage_OmitsCursorParamOnFirstPage(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "fetchChangesetsPage")

	if !strings.Contains(fn, "if (cursor) { pairs.push(['cursor', cursor]); }") {
		t.Errorf("fetchChangesetsPage does not conditionally append the cursor param only when cursor is truthy:\n%s", fn)
	}
}

// TestTimelineJS_FetchChangesetsPage_ReportsChangesetsAndNextCursor verifies
// the fetch's onDone callback is handed both the page's changesets and its
// nextCursor — the wiring point that makes further pagination possible at
// all — for both the success and failure (non-200/parse-error/network-error)
// paths, where nextCursor must default to "" rather than being omitted or
// left stale.
func TestTimelineJS_FetchChangesetsPage_ReportsChangesetsAndNextCursor(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "fetchChangesetsPage")

	if !strings.Contains(fn, "parsed.changesets") {
		t.Errorf("fetchChangesetsPage does not read changesets off the parsed response:\n%s", fn)
	}
	if !strings.Contains(fn, "parsed.nextCursor") {
		t.Errorf("fetchChangesetsPage does not read nextCursor off the parsed response:\n%s", fn)
	}

	for _, want := range []string{
		"onDone({ changesets: [], nextCursor: '' })",
	} {
		if strings.Count(fn, want) < 1 {
			t.Errorf("fetchChangesetsPage does not fail safe to %q on a non-200/parse/network error:\n%s", want, fn)
		}
	}
}

// TestTimelineJS_LoadBackdrop_ReplacesStateFromFreshFirstPage verifies a
// full backdrop reload (initial load, a facet-filter change, or
// clearAllFilters) always replaces state.changesets and state.nextCursor
// from a fresh, empty-cursor first page — pagination state never survives a
// filter change or accumulates across an unrelated reload.
func TestTimelineJS_LoadBackdrop_ReplacesStateFromFreshFirstPage(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)

	loadBackdropFn := extractFunctionBody(t, body, "loadBackdrop")
	if !strings.Contains(loadBackdropFn, "fetchChangesetsPage('', function (page) {") {
		t.Errorf("loadBackdrop does not fetch a fresh first page (cursor '') via fetchChangesetsPage:\n%s", loadBackdropFn)
	}
	if !strings.Contains(loadBackdropFn, "renderBackdrop(page);") {
		t.Errorf("loadBackdrop's fetch callback does not hand the fetched page to renderBackdrop:\n%s", loadBackdropFn)
	}

	renderBackdropFn := extractFunctionBody(t, body, "renderBackdrop")
	if !strings.Contains(renderBackdropFn, "state.changesets = page.changesets;") {
		t.Errorf("renderBackdrop does not replace state.changesets from the fetched page:\n%s", renderBackdropFn)
	}
	if !strings.Contains(renderBackdropFn, "state.nextCursor = page.nextCursor;") {
		t.Errorf("renderBackdrop does not replace state.nextCursor from the fetched page:\n%s", renderBackdropFn)
	}
}

// TestTimelineJS_RenderFeed_RendersLoadMoreRowOnlyWhenNextCursorPresent
// verifies R25's core affordance: a "Load more" row is appended after the
// visible feed rows exactly when state.nextCursor is non-empty, and never
// during the loading state or either empty/no-match state (those already
// render their own single full-width row and return early).
func TestTimelineJS_RenderFeed_RendersLoadMoreRowOnlyWhenNextCursorPresent(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)

	if !strings.Contains(body, "function buildLoadMoreRow(") {
		t.Fatalf("served timeline.js has no buildLoadMoreRow — the Load more affordance has no row builder")
	}
	loadMoreRowFn := extractFunctionBody(t, body, "buildLoadMoreRow")
	if !strings.Contains(loadMoreRowFn, "document.createElement('tr')") {
		t.Errorf("buildLoadMoreRow does not build a <tr>:\n%s", loadMoreRowFn)
	}
	if !strings.Contains(loadMoreRowFn, "colSpan = FEED_COLUMN_COUNT") {
		t.Errorf("buildLoadMoreRow's <td> does not span every column via colSpan = FEED_COLUMN_COUNT:\n%s", loadMoreRowFn)
	}
	if !strings.Contains(loadMoreRowFn, "document.createElement('button')") {
		t.Errorf("buildLoadMoreRow does not build a button-triggered affordance:\n%s", loadMoreRowFn)
	}
	if !strings.Contains(loadMoreRowFn, "loadMore") {
		t.Errorf("buildLoadMoreRow's button is not wired to loadMore:\n%s", loadMoreRowFn)
	}

	renderFeedFn := extractFunctionBody(t, body, "renderFeed")
	if !strings.Contains(renderFeedFn, "if (state.nextCursor) { feedEls.list.appendChild(buildLoadMoreRow()); }") {
		t.Errorf("renderFeed does not append buildLoadMoreRow() guarded on state.nextCursor:\n%s", renderFeedFn)
	}

	// The Load more row must be appended strictly after the visible-rows
	// loop (i.e. outside the loading/total==0/visible==0 early-return
	// paths), so it can never render standing in for actual feed content.
	visibleLoopIdx := strings.Index(renderFeedFn, "visible.forEach(function (cs) { feedEls.list.appendChild(buildFeedRow(cs)); });")
	loadMoreGuardIdx := strings.Index(renderFeedFn, "if (state.nextCursor) { feedEls.list.appendChild(buildLoadMoreRow()); }")
	if visibleLoopIdx == -1 {
		t.Fatalf("could not locate the visible-rows render loop in renderFeed:\n%s", renderFeedFn)
	}
	if loadMoreGuardIdx <= visibleLoopIdx {
		t.Errorf("the Load more row must be appended after the visible-rows loop, not before/inside it:\n%s", renderFeedFn)
	}
}

// TestTimelineJS_LoadMore_FetchesMergesAndGuardsConcurrentFetch verifies
// loadMore(): it fetches the next page using the stored state.nextCursor,
// merges the result into state.changesets via mergeChangesetPage (never a
// full replace — Load more is additive), updates state.nextCursor from the
// new page, and guards against firing a second fetch while one is already
// in flight (a fast double-click on "Load more" must not race two fetches).
func TestTimelineJS_LoadMore_FetchesMergesAndGuardsConcurrentFetch(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)

	if !strings.Contains(body, "function loadMore(") {
		t.Fatalf("served timeline.js has no loadMore function")
	}
	fn := extractFunctionBody(t, body, "loadMore")

	if !strings.Contains(fn, "if (state.loadingMore || !state.nextCursor) { return; }") {
		t.Errorf("loadMore does not guard against a concurrent fetch or a missing cursor:\n%s", fn)
	}
	if !strings.Contains(fn, "state.loadingMore = true;") {
		t.Errorf("loadMore does not set the in-flight guard before fetching:\n%s", fn)
	}
	if !strings.Contains(fn, "fetchChangesetsPage(state.nextCursor,") {
		t.Errorf("loadMore does not fetch the next page using state.nextCursor:\n%s", fn)
	}
	if !strings.Contains(fn, "mergeChangesetPage(state.changesets, page.changesets)") {
		t.Errorf("loadMore does not merge the fetched page into state.changesets via mergeChangesetPage:\n%s", fn)
	}
	if !strings.Contains(fn, "state.nextCursor = page.nextCursor;") {
		t.Errorf("loadMore does not update state.nextCursor from the newly fetched page:\n%s", fn)
	}
	if !strings.Contains(fn, "state.loadingMore = false;") {
		t.Errorf("loadMore does not clear the in-flight guard once the fetch settles:\n%s", fn)
	}

	// state.changesets must never be reassigned to page.changesets directly
	// inside loadMore — that would silently drop everything already loaded.
	if strings.Contains(fn, "state.changesets = page.changesets;") {
		t.Errorf("loadMore replaces state.changesets wholesale instead of merging — already-loaded Changesets would be dropped:\n%s", fn)
	}
}

// TestTimelineJS_LoadMore_DiscardsStaleResponseAfterFilterReload verifies a
// loadMore() fetch that is still in flight when a full backdrop reload
// (loadBackdrop — an initial load, a facet-filter change, or
// clearAllFilters) fires can never clobber the fresh, differently-filtered
// state.changesets/state.nextCursor with its own now-stale page once it
// resolves. This mirrors the exact clickGeneration guard
// TestTimelineJS_GuardsAgainstStaleClickCallbacks (timeline_test.go) already
// verifies for the analogous stale-async-response hazard around
// onFlagClick: a module-scoped generation counter (backdropGeneration) is
// bumped on every loadBackdrop, loadMore captures the generation it fired
// under, and its onDone callback compares against the current generation
// before merging into state.
func TestTimelineJS_LoadMore_DiscardsStaleResponseAfterFilterReload(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)

	if !strings.Contains(body, "var backdropGeneration = 0;") {
		t.Fatal("missing module-scoped backdropGeneration counter — the guard has nothing to compare against")
	}

	loadBackdropFn := extractFunctionBody(t, body, "loadBackdrop")
	if !strings.Contains(loadBackdropFn, "backdropGeneration++") {
		t.Errorf("loadBackdrop does not bump backdropGeneration — a fresh reload never supersedes an in-flight loadMore:\n%s", loadBackdropFn)
	}
	if !strings.Contains(loadBackdropFn, "gen = backdropGeneration") {
		t.Errorf("loadBackdrop does not capture the generation it fired under:\n%s", loadBackdropFn)
	}
	if !strings.Contains(loadBackdropFn, "if (gen !== backdropGeneration) { return; }") {
		t.Errorf("loadBackdrop's own fetch callback does not guard against a superseded reload before calling renderBackdrop:\n%s", loadBackdropFn)
	}

	loadMoreFn := extractFunctionBody(t, body, "loadMore")
	if !strings.Contains(loadMoreFn, "gen = backdropGeneration") {
		t.Errorf("loadMore does not capture the generation it fired under:\n%s", loadMoreFn)
	}

	pageCallback := extractCallbackAfter(t, loadMoreFn, "fetchChangesetsPage(state.nextCursor,")
	guardIdx := strings.Index(pageCallback, "gen !== backdropGeneration")
	mergeIdx := strings.Index(pageCallback, "mergeChangesetPage(")
	if guardIdx == -1 {
		t.Errorf("loadMore's onDone callback does not guard against a superseded generation before merging:\n%s", pageCallback)
	} else if mergeIdx == -1 || guardIdx > mergeIdx {
		t.Errorf("the generation guard must run before mergeChangesetPage mutates state:\n%s", pageCallback)
	}
	if !strings.Contains(pageCallback, "state.loadingMore = false;") {
		t.Errorf("loadMore's onDone callback does not clear state.loadingMore on the stale-discard path, which would wedge loadMore forever:\n%s", pageCallback)
	}
}

// TestTimelineJS_LoadMore_ExtendsWindowOnlyWhenNotManuallyZoomed verifies
// the window-sensibility behavior: when the visible window already covers
// the entire previously-loaded data span (the common, unzoomed case),
// loadMore re-fits the window so the newly merged (older) Changesets
// actually become visible; when the user has manually zoomed to a
// sub-range, loadMore leaves the window untouched so their view isn't
// yanked out from under them.
func TestTimelineJS_LoadMore_ExtendsWindowOnlyWhenNotManuallyZoomed(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "loadMore")

	if !strings.Contains(fn, "windowCoversAllData()") {
		t.Errorf("loadMore does not check windowCoversAllData() before deciding whether to re-fit the window:\n%s", fn)
	}
	if !strings.Contains(fn, "fitWindowToData();") {
		t.Errorf("loadMore does not re-fit the window when it previously covered all data:\n%s", fn)
	}
	if !strings.Contains(fn, "afterWindowChange();") {
		t.Errorf("loadMore does not re-render (afterWindowChange) after merging the new page:\n%s", fn)
	}
}
