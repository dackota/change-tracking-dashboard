// Package web (this file): behavioral coverage for the feed-table slice —
// the migration of the served timeline.js's feed-row rendering from <li>
// list items to real <tr>/<td> table rows (R9/R10), and the loading/empty/
// no-match states rendered in table form (R16). Every assertion here follows
// the structural-source pattern already established by
// TestTimelineJS_GuardsAgainstStaleClickCallbacks (helpers_test.go's
// extractFunctionBody/extractCallbackAfter): the repo has no browser/DOM
// test harness, so client-side control flow is verified against the exact
// source served at /static/timeline.js — the same bytes a browser executes.
// The one piece of pure, DOM-free logic (the commit-link derivation) has its
// own dedicated property test run under Node — see
// static/commit-link.property.test.js — which this file does not duplicate.
package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// servedTimelineJS fetches the exact bytes served at /static/timeline.js.
func servedTimelineJS(t *testing.T) string {
	t.Helper()
	h := web.NewStaticHandler()
	req := httptest.NewRequest(http.MethodGet, "/static/timeline.js", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	return rr.Body.String()
}

// TestTimelineJS_BuildFeedRow_CreatesTableRowNotListItem verifies R9: the
// feed's per-Changeset row builder now constructs a <tr> (with <td> cells),
// not the old <li> — the feed body is real table markup, not a list dressed
// up to look like one.
func TestTimelineJS_BuildFeedRow_CreatesTableRowNotListItem(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	if !strings.Contains(fn, "document.createElement('tr')") {
		t.Errorf("buildFeedRow does not create a <tr> — row is not table markup:\n%s", fn)
	}
	if strings.Contains(fn, "document.createElement('li')") {
		t.Errorf("buildFeedRow still creates an <li> — the list-item migration to table rows is incomplete:\n%s", fn)
	}

	tdCount := strings.Count(fn, "document.createElement('td')")
	if tdCount != 6 {
		t.Errorf("buildFeedRow creates %d <td> cells, want 6 (When, Repository, Commit, Author, Changes, Risk):\n%s", tdCount, fn)
	}
}

// TestTimelineJS_BuildFeedRow_CellsCarryTheRequiredFields verifies R10 (and
// R24's feed-badge wiring): each row's cells are sourced from the day/time
// stamp, short repository name, commit sha/URL, author, per-Changeset change
// count, and risk class(es) — the same data the old <li> rendering used (plus
// the risk badge this slice adds), now distributed across <td> cells in the
// order the thead declares (When, Repository, Commit, Author, Changes, Risk).
func TestTimelineJS_BuildFeedRow_CellsCarryTheRequiredFields(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	for _, want := range []string{
		"fmtDateTime(csTime(cs))",          // When
		"repoShortName(cs.repo)",           // Repository
		"commitURL(cs.repo, cs.commitSha)", // Commit
		"cs.author",                        // Author
		"cs.changes",                       // Changes (count)
		"cs.risk",                          // Risk badge(s)
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("buildFeedRow missing expected data source %q:\n%s", want, fn)
		}
	}
}

// TestTimelineJS_BuildFeedRow_UsesTextContentNeverInnerHTML verifies the R19
// carry-over security invariant scoped to the new row builder: client-derived
// strings (repo, author, sha, counts, timestamps) must be set via
// textContent, never string-concatenated into innerHTML — no new unescaped
// sink is introduced by the table migration.
func TestTimelineJS_BuildFeedRow_UsesTextContentNeverInnerHTML(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	if strings.Contains(fn, "innerHTML") {
		t.Errorf("buildFeedRow uses innerHTML — client-derived cell data must be assigned via textContent only:\n%s", fn)
	}
	if strings.Contains(fn, "insertAdjacentHTML") {
		t.Errorf("buildFeedRow uses insertAdjacentHTML — client-derived cell data must be assigned via textContent only:\n%s", fn)
	}

	textContentAssignments := strings.Count(fn, ".textContent =")
	if textContentAssignments < 4 {
		t.Errorf("buildFeedRow has only %d .textContent assignments, expected at least 4 (time/repo/author/count, plus sha via one of the link/plain branches):\n%s", textContentAssignments, fn)
	}
}

// TestTimelineJS_BuildFeedRow_CommitCellLinksHTTPRepoPlainTextLocalRepo
// verifies R10's commit-link branch is actually wired into the row builder:
// an <a> built via safe property assignment (href/textContent) when
// commitURL(...) is truthy (http(s) repo), and a plain non-link element
// carrying the sha as textContent when it is falsy (local-path repo) — never
// a raw HTML string built from the sha.
func TestTimelineJS_BuildFeedRow_CommitCellLinksHTTPRepoPlainTextLocalRepo(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	if !strings.Contains(fn, "var url = commitURL(cs.repo, cs.commitSha);") {
		t.Fatalf("buildFeedRow does not derive the commit URL via commitURL(cs.repo, cs.commitSha):\n%s", fn)
	}
	if !strings.Contains(fn, "if (url)") {
		t.Fatalf("buildFeedRow does not branch on the derived commit URL's truthiness:\n%s", fn)
	}

	linkBranch := fn[strings.Index(fn, "if (url)"):]
	elseIdx := strings.Index(linkBranch, "} else {")
	if elseIdx == -1 {
		t.Fatalf("buildFeedRow's commit-link branch has no else (plain-text) arm:\n%s", fn)
	}
	linkArm, plainArm := linkBranch[:elseIdx], linkBranch[elseIdx:]

	if !strings.Contains(linkArm, "document.createElement('a')") {
		t.Errorf("the linked (http(s)) arm does not create an <a>:\n%s", linkArm)
	}
	if !strings.Contains(linkArm, "a.href = url") {
		t.Errorf("the linked arm does not set href via safe property assignment:\n%s", linkArm)
	}
	if !strings.Contains(linkArm, "a.textContent = sha") {
		t.Errorf("the linked arm does not set the visible sha via textContent:\n%s", linkArm)
	}

	if strings.Contains(plainArm, "document.createElement('a')") {
		t.Errorf("the local-path (non-linked) arm must not create an <a> — plain text only:\n%s", plainArm)
	}
	if !strings.Contains(plainArm, ".textContent = sha") {
		t.Errorf("the local-path arm does not render the sha as plain textContent:\n%s", plainArm)
	}
}

// TestTimelineJS_BuildFeedRow_PreservesClickToDetailAndLinkStopPropagation
// verifies the PRD's explicit non-regression requirement: a feed row stays
// clickable to open its Changeset detail exactly as the old <li> did, and
// clicking the commit link itself does not also trigger the row's detail
// click (stopPropagation), so the link's own navigation isn't swallowed by a
// detail-panel open.
func TestTimelineJS_BuildFeedRow_PreservesClickToDetailAndLinkStopPropagation(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	if !strings.Contains(fn, "tr.addEventListener('click', function () { onFlagClick([cs]); });") {
		t.Errorf("buildFeedRow's <tr> does not wire the click-to-detail handler onFlagClick([cs]):\n%s", fn)
	}

	linkCallback := extractCallbackAfter(t, fn, "a.addEventListener('click',")
	if !strings.Contains(linkCallback, "e.stopPropagation()") {
		t.Errorf("the commit link's click handler does not stopPropagation — clicking it would also open the row's detail:\n%s", linkCallback)
	}
}

// TestTimelineJS_BuildFeedRow_ShowsSubjectAboveShaWhenPresent verifies #85:
// the commit cell renders the Changeset's subject as a distinct, tooltip-
// bearing label (so a long subject can truncate via CSS while the full text
// stays available on hover), sourced from cs.subject and set via textContent
// (never innerHTML) before the sha element it never replaces — see the sha-
// always-rendered test below for the fallback contract.
func TestTimelineJS_BuildFeedRow_ShowsSubjectAboveShaWhenPresent(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	if !strings.Contains(fn, "cs.subject") {
		t.Fatalf("buildFeedRow does not read cs.subject:\n%s", fn)
	}
	if !strings.Contains(fn, "feed-commit-subject") {
		t.Errorf("buildFeedRow does not create a feed-commit-subject element:\n%s", fn)
	}
	if !strings.Contains(fn, "subjectEl.textContent = cs.subject") {
		t.Errorf("buildFeedRow does not set the subject label's textContent from cs.subject:\n%s", fn)
	}
	if !strings.Contains(fn, "subjectEl.title = cs.subject") {
		t.Errorf("buildFeedRow does not set a title tooltip on the subject label (needed for truncated long subjects):\n%s", fn)
	}

	subjectIdx := strings.Index(fn, "feed-commit-subject")
	urlIdx := strings.Index(fn, "if (url)")
	if subjectIdx == -1 || urlIdx == -1 || subjectIdx > urlIdx {
		t.Errorf("subject label must be built before the sha link/plain-text branch:\n%s", fn)
	}
}

// TestTimelineJS_BuildFeedRow_ShaAlwaysRendered_FallsBackWhenNoSubject
// verifies #85's acceptance criterion "rows with no stored subject fall
// back to the SHA": the sha element (link or plain text) is built
// unconditionally — it is never nested inside an `if (cs.subject)` guard —
// so a Changeset with no recorded subject still shows its sha exactly as
// before this slice.
func TestTimelineJS_BuildFeedRow_ShaAlwaysRendered_FallsBackWhenNoSubject(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	if !strings.Contains(fn, "if (cs.subject)") {
		t.Fatalf("buildFeedRow does not guard the subject label behind if (cs.subject):\n%s", fn)
	}

	// The subject guard's own block must not be where the sha gets built —
	// isolate it by depth-counted brace span and assert the sha assignments
	// live outside of it.
	guardIdx := strings.Index(fn, "if (cs.subject)")
	braceIdx := strings.Index(fn[guardIdx:], "{")
	if braceIdx == -1 {
		t.Fatalf("if (cs.subject) has no block:\n%s", fn)
	}
	depth := 0
	end := -1
	for i := guardIdx + braceIdx; i < len(fn); i++ {
		switch fn[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if end != -1 {
			break
		}
	}
	if end == -1 {
		t.Fatalf("could not find matching closing brace for if (cs.subject):\n%s", fn)
	}
	subjectGuardBlock := fn[guardIdx:end]

	if strings.Contains(subjectGuardBlock, "a.textContent = sha") || strings.Contains(subjectGuardBlock, "shaEl.textContent = sha") {
		t.Errorf("sha rendering must live outside the if (cs.subject) guard — it must render even with no subject:\n%s", subjectGuardBlock)
	}
	if !strings.Contains(fn, "a.textContent = sha") || !strings.Contains(fn, "shaEl.textContent = sha") {
		t.Errorf("buildFeedRow must still render the sha (link and plain-text arms) unconditionally:\n%s", fn)
	}
}

// TestTimelineJS_RenderFeed_LoadingAndEmptyStatesRenderAsFullWidthTableRows
// verifies R16 in its new "table form": the loading, nothing-recorded-yet,
// and nothing-in-window/filters states are each rendered as a single
// full-width row (one <td colspan="6"> spanning every column) appended
// directly into the <tbody id="feed-list"> — not a bare table (headers with
// nothing sensible under them) and not a mechanism that only worked by
// accident for a <ul>-shaped feed.
func TestTimelineJS_RenderFeed_LoadingAndEmptyStatesRenderAsFullWidthTableRows(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)

	if !strings.Contains(body, "function buildEmptyRow(") {
		t.Fatalf("served timeline.js has no buildEmptyRow — the empty/loading state has no in-table row builder")
	}
	emptyRowFn := extractFunctionBody(t, body, "buildEmptyRow")
	if !strings.Contains(emptyRowFn, "document.createElement('tr')") {
		t.Errorf("buildEmptyRow does not build a <tr>:\n%s", emptyRowFn)
	}
	if !strings.Contains(emptyRowFn, "colSpan = FEED_COLUMN_COUNT") {
		t.Errorf("buildEmptyRow's <td> does not span every column via colSpan = FEED_COLUMN_COUNT:\n%s", emptyRowFn)
	}

	renderFeedFn := extractFunctionBody(t, body, "renderFeed")
	for _, want := range []string{
		"buildEmptyRow('Loading changes…', false)",
		"buildEmptyRow('No changes recorded yet",
		"buildEmptyRow('No changes in this window",
	} {
		if !strings.Contains(renderFeedFn, want) {
			t.Errorf("renderFeed does not render the expected in-table empty row for %q:\n%s", want, renderFeedFn)
		}
	}

	// The old sibling-div swap mechanism (a separate #feed-empty element
	// toggled independently of the table body) must be fully retired — the
	// empty/loading states now live inside the table itself.
	if strings.Contains(body, "feedEls.empty") {
		t.Errorf("served timeline.js still references feedEls.empty — the old sibling-div empty state was not removed")
	}
}

// TestTimelineJS_RenderFeed_NoMatchStateOffersClearAllButLoadingDoesNot
// verifies the exact clear-all affordance rules are preserved across the
// table migration: the loading state never offers a clear-all action; the
// nothing-in-window/filters state always does (R16c); the nothing-recorded-
// yet state offers it only when a facet filter is active (clearing filters
// can't produce data that was never recorded).
func TestTimelineJS_RenderFeed_NoMatchStateOffersClearAllButLoadingDoesNot(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	renderFeedFn := extractFunctionBody(t, body, "renderFeed")

	if !strings.Contains(renderFeedFn, "buildEmptyRow('Loading changes…', false)") {
		t.Errorf("the loading state must never offer a clear-all affordance:\n%s", renderFeedFn)
	}
	if !strings.Contains(renderFeedFn, "buildEmptyRow('No changes recorded yet — the poller may still be backfilling.', activeFilterCount() > 0)") {
		t.Errorf("the nothing-recorded-yet state must offer clear-all only when a filter is active:\n%s", renderFeedFn)
	}
	noMatchCall := "buildEmptyRow('No changes in this window' + (activeFilterCount() > 0 ? ' or matching the current filters.' : '.'), true)"
	if !strings.Contains(renderFeedFn, noMatchCall) {
		t.Errorf("the nothing-in-window/filters state must always offer clear-all:\nwant call %q in:\n%s", noMatchCall, renderFeedFn)
	}
}
