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
// all — for the success path, where nextCursor must default to "" rather
// than being omitted or left stale.
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
}

// TestTimelineJS_FetchChangesetsPage_FailureReportsErrorDistinctFromEndOfData
// verifies a fetch failure (non-200, malformed JSON, or a network error)
// reports error: true alongside an empty page — deliberately distinct from a
// genuinely successful response with an empty nextCursor (the server's real
// end-of-data signal). Collapsing both to the same
// `{ changesets: [], nextCursor: "" }` shape (the pre-fix behavior) made a
// transient hiccup during "Load more" indistinguishable from having reached
// the end of the data, silently and permanently removing the Load more
// control with no error surfaced.
func TestTimelineJS_FetchChangesetsPage_FailureReportsErrorDistinctFromEndOfData(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "fetchChangesetsPage")

	for _, want := range []string{
		"onDone({ changesets: [], nextCursor: '', error: true })",
	} {
		if strings.Count(fn, want) < 3 {
			t.Errorf("fetchChangesetsPage does not report %q on all three failure paths (non-200, parse error, network error):\n%s", want, fn)
		}
	}
	// The success path must never set error, so a caller can safely test
	// `if (page.error)` without also checking nextCursor.
	if !strings.Contains(fn, "onDone({ changesets: parsed.changesets || [], nextCursor: parsed.nextCursor || '' });") {
		t.Errorf("fetchChangesetsPage's success path does not report a plain {changesets, nextCursor} page with no error flag:\n%s", fn)
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
// verifies R25's core affordance: a "Load more" row is appended exactly when
// state.nextCursor is non-empty — both after the visible feed rows (the
// common case) AND after the zero-visible-window empty state (a user zoomed
// into a sub-range with no currently-visible Changesets must still be able
// to page in the next, older batch rather than being forced to reset zoom
// first) — but never during the loading state or the "nothing loaded at
// all" (total == 0) empty state, which return before either call site.
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

	if !strings.Contains(body, "function maybeAppendLoadMoreRow(") {
		t.Fatalf("served timeline.js has no maybeAppendLoadMoreRow — expected a single chokepoint guarding the Load more row on state.nextCursor")
	}
	maybeAppendFn := extractFunctionBody(t, body, "maybeAppendLoadMoreRow")
	if !strings.Contains(maybeAppendFn, "if (state.nextCursor) { feedEls.list.appendChild(buildLoadMoreRow()); }") {
		t.Errorf("maybeAppendLoadMoreRow does not guard appending buildLoadMoreRow() on state.nextCursor:\n%s", maybeAppendFn)
	}

	renderFeedFn := extractFunctionBody(t, body, "renderFeed")

	// maybeAppendLoadMoreRow() must be called from exactly two places: once
	// after the zero-visible-window empty state (so a user zoomed into an
	// empty sub-range can still page in more data), and once after the
	// visible-rows loop (the common case) — never inside the loading or
	// total==0 ("nothing loaded at all") early returns.
	zeroVisibleIdx := strings.Index(renderFeedFn, "No changes in this window")
	visibleLoopIdx := strings.Index(renderFeedFn, "visible.forEach(function (cs) { feedEls.list.appendChild(buildFeedRow(cs)); });")
	if zeroVisibleIdx == -1 {
		t.Fatalf("could not locate the zero-visible-window empty state in renderFeed:\n%s", renderFeedFn)
	}
	if visibleLoopIdx == -1 {
		t.Fatalf("could not locate the visible-rows render loop in renderFeed:\n%s", renderFeedFn)
	}

	firstCallIdx := strings.Index(renderFeedFn, "maybeAppendLoadMoreRow();")
	if firstCallIdx == -1 {
		t.Fatalf("renderFeed does not call maybeAppendLoadMoreRow() at all:\n%s", renderFeedFn)
	}
	secondCallRel := strings.Index(renderFeedFn[firstCallIdx+1:], "maybeAppendLoadMoreRow();")
	if secondCallRel == -1 {
		t.Fatalf("expected renderFeed to call maybeAppendLoadMoreRow() twice (zero-visible-window branch and after the visible-rows loop), found only once:\n%s", renderFeedFn)
	}
	secondCallIdx := secondCallRel + firstCallIdx + 1

	if firstCallIdx <= zeroVisibleIdx || firstCallIdx >= visibleLoopIdx {
		t.Errorf("expected the first maybeAppendLoadMoreRow() call to sit inside the zero-visible-window branch (after its empty-row message, before the visible-rows loop):\n%s", renderFeedFn)
	}
	if secondCallIdx <= visibleLoopIdx {
		t.Errorf("expected the second maybeAppendLoadMoreRow() call to come after the visible-rows loop:\n%s", renderFeedFn)
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

// TestTimelineJS_LoadMore_RecomputesWindowRefitDecisionAtUseTime is a
// correctness-gate regression test: windowCoversAllData() must be evaluated
// INSIDE the fetchChangesetsPage callback (i.e. once the response actually
// arrives), never captured in a variable before the async fetch fires. A
// decision captured at request-time can go stale — the user can
// drag-zoom/wheel-zoom/pan/edit the From-To inputs while the "Load more"
// fetch is in flight, since none of those are disabled during it — and a
// stale `true` would silently discard a zoom/pan the user just made once the
// response arrives. See load-more.behavior.test.js for the corresponding
// async-timing behavioral proof (this repo has no DOM test harness, so that
// file exercises the real callback timing; this test locks the static
// source shape that makes it possible).
func TestTimelineJS_LoadMore_RecomputesWindowRefitDecisionAtUseTime(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	loadMoreFn := extractFunctionBody(t, body, "loadMore")

	callSiteIdx := strings.Index(loadMoreFn, "fetchChangesetsPage(state.nextCursor,")
	if callSiteIdx == -1 {
		t.Fatalf("could not locate the fetchChangesetsPage call site in loadMore:\n%s", loadMoreFn)
	}
	// windowCoversAllData() must not appear before the call site (that would
	// mean it's captured pre-fetch); it must appear inside the callback that
	// follows.
	if idx := strings.Index(loadMoreFn[:callSiteIdx], "windowCoversAllData()"); idx != -1 {
		t.Errorf("windowCoversAllData() is evaluated BEFORE fetchChangesetsPage fires (request-time), not inside its callback (use-time) — this is the stale-snapshot race:\n%s", loadMoreFn)
	}

	pageCallback := extractCallbackAfter(t, loadMoreFn, "fetchChangesetsPage(state.nextCursor,")
	refitIdx := strings.Index(pageCallback, "windowCoversAllData()")
	mergeIdx := strings.Index(pageCallback, "mergeChangesetPage(")
	fitIdx := strings.Index(pageCallback, "fitWindowToData();")
	if refitIdx == -1 {
		t.Fatalf("loadMore's onDone callback does not evaluate windowCoversAllData():\n%s", pageCallback)
	}
	if mergeIdx == -1 {
		t.Fatalf("could not locate mergeChangesetPage( in loadMore's onDone callback:\n%s", pageCallback)
	}
	if fitIdx == -1 {
		t.Fatalf("could not locate fitWindowToData(); in loadMore's onDone callback:\n%s", pageCallback)
	}
	// windowCoversAllData() must be evaluated against the PRE-merge span: the
	// newly-fetched page is, by construction, older than everything already
	// loaded, so checking the POST-merge span would almost always find the
	// window no longer covers it — defeating the refit's purpose (making the
	// newly-loaded, older Changesets actually visible). See loadMore's own
	// doc comment for the full rationale.
	if refitIdx > mergeIdx {
		t.Errorf("windowCoversAllData() must be evaluated BEFORE mergeChangesetPage(...) reassigns state.changesets, or it measures the wrong (post-merge) span:\n%s", pageCallback)
	}
	if fitIdx < mergeIdx {
		t.Errorf("fitWindowToData() must run AFTER the page is merged into state.changesets, or it fits against stale data:\n%s", pageCallback)
	}
}

// TestTimelineJS_LoadMore_DistinguishesFetchFailureFromEndOfData verifies a
// fetch failure (page.error) during "Load more" is handled distinctly from a
// real end-of-data response: state.nextCursor must be left untouched
// (never overwritten with the same empty string a legitimate last-page response
// reports, which would permanently and silently remove the Load more
// control on a transient hiccup) and state.loadMoreError must be set so the
// UI can surface it.
func TestTimelineJS_LoadMore_DistinguishesFetchFailureFromEndOfData(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	loadMoreFn := extractFunctionBody(t, body, "loadMore")
	pageCallback := extractCallbackAfter(t, loadMoreFn, "fetchChangesetsPage(state.nextCursor,")

	errorGuardIdx := strings.Index(pageCallback, "if (page.error)")
	if errorGuardIdx == -1 {
		t.Fatalf("loadMore's onDone callback does not branch on page.error:\n%s", pageCallback)
	}
	errorBranch, ok := braceDelimitedSpan(pageCallback, errorGuardIdx)
	if !ok {
		t.Fatalf("could not find the page.error branch body:\n%s", pageCallback)
	}
	if !strings.Contains(errorBranch, "state.loadMoreError = true;") {
		t.Errorf("loadMore's page.error branch does not set state.loadMoreError:\n%s", errorBranch)
	}
	if strings.Contains(errorBranch, "state.nextCursor") {
		t.Errorf("loadMore's page.error branch must not touch state.nextCursor — overwriting it with the failure page's empty cursor would be indistinguishable from real end-of-data:\n%s", errorBranch)
	}
	if strings.Contains(errorBranch, "mergeChangesetPage(") {
		t.Errorf("loadMore's page.error branch must not merge the (empty) failure page into state.changesets:\n%s", errorBranch)
	}
	if !strings.Contains(errorBranch, "return;") {
		t.Errorf("loadMore's page.error branch does not return, and would fall through into the success-path merge/refit logic:\n%s", errorBranch)
	}

	// The nextCursor assignment must exist exactly once outside the error
	// branch — on the success path only.
	if strings.Count(pageCallback, "state.nextCursor = page.nextCursor;") != 1 {
		t.Errorf("expected exactly one (success-path) state.nextCursor assignment in loadMore's onDone callback:\n%s", pageCallback)
	}
}
