package web_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/pollstatus"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// TestTimelineHandler_ReturnsHTMLShell verifies the tracer bullet: GET / on
// the timeline handler returns 200, text/html, and the page references the
// embedded timeline script by a first-party path (never an external CDN URL).
func TestTimelineHandler_ReturnsHTMLShell(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t), pollstatus.New())

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

	h := web.NewTimelineHandler(st, pollstatus.New())
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

// TestTimelineHandler_ReservedFacetNameNeverRendersAsControl is a table test
// over the full reserved-name set {repo, asOf, cursor, limit}: none of them
// may ever render as a tri-state facet control, even when a stored Change
// carries a facet keyed with exactly that reserved name. Rendering one would
// let an admin-configured facet named "repo" (etc.) collide with the
// dedicated repo-scope dropdown/reserved query param — the repo-param
// facet-shadowing defect this test guards against. A co-seeded non-reserved
// facet ("region") must still render, proving the exclusion is narrowly
// scoped to the reserved set only.
func TestTimelineHandler_ReservedFacetNameNeverRendersAsControl(t *testing.T) {
	t.Parallel()

	reservedNames := []string{"repo", "asOf", "cursor", "limit"}

	st := newTestStore(t)
	for i, name := range reservedNames {
		seedChangeWithFacets(t, st, fmt.Sprintf("commit-reserved-%d", i), map[string]string{
			name:     "hijack-value",
			"region": "us-west-2",
		})
	}

	h := web.NewTimelineHandler(st, pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, name := range reservedNames {
		t.Run(name, func(t *testing.T) {
			wantAttr := fmt.Sprintf(`data-facet="%s"`, name)
			if strings.Contains(body, wantAttr) {
				t.Errorf("body rendered a control for reserved facet name %q (attr %q present), want excluded", name, wantAttr)
			}
		})
	}

	if !strings.Contains(body, `data-facet="region" data-value="us-west-2"`) {
		t.Errorf("body missing control for non-reserved facet region=us-west-2 (exclusion must not affect non-reserved facets); got:\n%s", body)
	}
}

// TestTimelineHandler_EmptyStore_RendersNoFacetControlsWithoutError verifies
// that an empty store (no Changes, so no known facets) still renders the
// shell successfully with no facet controls — an edge case that must not
// panic or 500.
func TestTimelineHandler_EmptyStore_RendersNoFacetControlsWithoutError(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t), pollstatus.New())
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

	h := web.NewTimelineHandler(st, pollstatus.New())
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

	h := web.NewTimelineHandler(st, pollstatus.New())
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

// TestTimelineHandler_RendersOneRepoOptionPerTrackedRepo verifies R26: the
// repository filter dropdown is populated from store.RepositoryStats() — one
// <option> per tracked repository, reusing that existing per-repo query
// rather than a second repo-listing query.
func TestTimelineHandler_RendersOneRepoOptionPerTrackedRepo(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedChange(t, st, changeSpec{Repo: "apps-repo"})
	seedChange(t, st, changeSpec{Repo: "infra-repo"})

	h := web.NewTimelineHandler(st, pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `id="repo-filter"`) {
		t.Fatalf("body missing repo filter dropdown (id=\"repo-filter\"); got:\n%s", body)
	}
	for _, wantOption := range []string{
		`<option value="apps-repo">apps-repo</option>`,
		`<option value="infra-repo">infra-repo</option>`,
	} {
		if !strings.Contains(body, wantOption) {
			t.Errorf("body missing repo option %q; got:\n%s", wantOption, body)
		}
	}
}

// TestTimelineHandler_EmptyStore_RendersRepoFilterWithOnlyAllOption verifies
// that an empty store (no Changes, so no tracked repos) still renders the
// repo filter dropdown with just its "All repositories" default option —
// never a panic or 500.
func TestTimelineHandler_EmptyStore_RendersRepoFilterWithOnlyAllOption(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t), pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `id="repo-filter"`) {
		t.Fatalf("body missing repo filter dropdown (id=\"repo-filter\"); got:\n%s", body)
	}
	if strings.Contains(body, `<option value="apps-repo"`) {
		t.Errorf("expected no repo options for an empty store; got:\n%s", body)
	}
}

// TestTimelineHandler_RepoNameIsHTMLEscaped verifies that a repo name
// containing HTML-significant characters is rendered escaped (via
// html/template auto-escaping), never string-concatenated raw into the page.
func TestTimelineHandler_RepoNameIsHTMLEscaped(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	const maliciousRepo = `"><script>alert(1)</script>`
	seedChange(t, st, changeSpec{Repo: maliciousRepo})

	h := web.NewTimelineHandler(st, pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("repo name rendered unescaped — raw <script> tag present in body:\n%s", body)
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

// TestTimelineJS_GuardsAgainstStaleClickCallbacks verifies the served
// timeline.js enforces the click-generation invariant: once a new
// onFlagClick call supersedes a prior one, no async callback belonging to
// the superseded click may mutate the detail panel or initiate any further
// network request. A module-scoped clickGeneration counter is bumped on
// every onFlagClick; every async continuation reachable from a click
// (fetchChangesetDetail's onDone in onFlagClick itself, and
// fetchChartDiff's onDone in loadChartDiffsForChangeset) captures the
// generation it fired under and compares it against the current
// clickGeneration before touching the DOM — a callback whose click was
// superseded becomes a no-op instead of mutating a panel/slot that now
// belongs to a different click.
func TestTimelineJS_GuardsAgainstStaleClickCallbacks(t *testing.T) {
	t.Parallel()

	h := web.NewStaticHandler()
	req := httptest.NewRequest(http.MethodGet, "/static/timeline.js", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	if !strings.Contains(body, "var clickGeneration = 0;") {
		t.Fatal("missing module-scoped clickGeneration counter — the guard has nothing to compare against")
	}

	onFlagClick := extractFunctionBody(t, body, "onFlagClick")
	if !strings.Contains(onFlagClick, "clickGeneration++") {
		t.Error("onFlagClick does not bump clickGeneration — a new click never supersedes a prior one")
	}
	if !strings.Contains(onFlagClick, "gen = clickGeneration") {
		t.Error("onFlagClick does not capture the generation it fired under — its callbacks have no stable value to compare against")
	}

	detailCallback := extractCallbackAfter(t, onFlagClick, "fetchChangesetDetail(")
	guardIdx := strings.Index(detailCallback, "gen !== clickGeneration")
	mutateIdx := strings.Index(detailCallback, "insertAdjacentHTML")
	if guardIdx == -1 {
		t.Error("fetchChangesetDetail's onDone callback does not guard against a superseded click before mutating the panel")
	} else if mutateIdx == -1 || guardIdx > mutateIdx {
		t.Error("the generation guard must run before insertAdjacentHTML mutates the panel")
	}

	loadChartDiffs := extractFunctionBody(t, body, "loadChartDiffsForChangeset")
	if !strings.Contains(loadChartDiffs, "function loadChartDiffsForChangeset(subtree, cs, gen)") {
		t.Error("loadChartDiffsForChangeset does not accept the gen the click fired under — it can't guard its own async callbacks")
	}

	chartCallback := extractCallbackAfter(t, loadChartDiffs, "fetchChartDiff(")
	guardIdx = strings.Index(chartCallback, "gen !== clickGeneration")
	mutateIdx = strings.Index(chartCallback, "innerHTML")
	if guardIdx == -1 {
		t.Error("fetchChartDiff's onDone callback does not guard against a superseded click before mutating its slot")
	} else if mutateIdx == -1 || guardIdx > mutateIdx {
		t.Error("the generation guard must run before innerHTML mutates the chart-diff slot")
	}
}

// TestTimelineHandler_CSPPermitsOnlySelfScript verifies the CSP header on
// GET / permits script execution only from 'self' (the embedded, first-party
// script) — never an external origin, and never 'unsafe-inline'/
// 'unsafe-eval' — while the other conservative security headers stay intact.
func TestTimelineHandler_CSPPermitsOnlySelfScript(t *testing.T) {
	t.Parallel()

	h := web.NewTimelineHandler(newTestStore(t), pollstatus.New())
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

	h := web.NewTimelineHandler(newTestStore(t), pollstatus.New())
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

// TestTimelineHandler_HeaderPollChip_RendersAcrossEveryPage verifies R11/R6:
// the Timeline page's header (the same shared shell every route builds)
// renders the aggregate poll-status chip too — not just the Trackers page —
// reflecting the injected poll-status registry's current state.
func TestTimelineHandler_HeaderPollChip_RendersAcrossEveryPage(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()
	tr := domain.Tracker{Repo: "repo-a", FileGlob: "Chart.yaml", Field: "version", PollIntervalSeconds: 60}
	reg.Record(tr, time.Now(), errors.New("network unreachable"))

	h := web.NewTimelineHandler(newTestStore(t), reg)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, `data-poll-status="error"`) {
		t.Errorf("Timeline header missing error-state poll chip; got:\n%s", body)
	}
	if !strings.Contains(body, "1 tracker failing") {
		t.Errorf("Timeline header chip missing error summary text; got:\n%s", body)
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
