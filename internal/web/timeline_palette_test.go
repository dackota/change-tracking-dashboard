// Package web (this file): behavioral coverage for the visual-system-
// regression slice's palette unification (R17). Before this slice, the
// shell (sidebar/header/KPI tiles) used the --oc-* option-C tokens while the
// facet controls, timeline chrome, feed, detail view, and Chart diff still
// used a separate, older token set (--ink/--muted/--line/--line-soft/--blue/
// --red/--surface/--bg) — two parallel palettes on one page. This file
// asserts, against the exact bytes GET / serves, that the old tokens are
// fully retired (no declaration, no reference) and that the detail-view and
// Chart-diff rules — the two areas the PRD calls out by name — read their
// colors from --oc-* custom properties instead.
package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// servedTimelinePage fetches the exact bytes GET / serves — the same shell
// markup (and its single inline <style>) a browser receives.
func servedTimelinePage(t *testing.T) string {
	t.Helper()
	h := web.NewTimelineHandler(newTestStore(t), pollstatus.New())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	return rr.Body.String()
}

// TestTimelineTemplate_RetiresLegacyPaletteTokens verifies the old,
// pre-option-C token set is fully retired rather than left as a second,
// parallel palette: neither its :root declarations nor any var(...) usage
// survive anywhere in the served page.
func TestTimelineTemplate_RetiresLegacyPaletteTokens(t *testing.T) {
	t.Parallel()

	body := servedTimelinePage(t)

	// No custom-property declaration for any retired token name.
	for _, declaration := range []string{
		"--ink:", "--muted:", "--line:", "--line-soft:",
		"--blue:", "--red:", "--surface:", "--bg:",
	} {
		if strings.Contains(body, declaration) {
			t.Errorf("served page still declares retired palette token %q — two parallel palettes remain", declaration)
		}
	}

	// No rule still reads a retired token via var(...).
	for _, usage := range []string{
		"var(--ink)", "var(--muted)", "var(--line)", "var(--line-soft)",
		"var(--blue)", "var(--red)", "var(--surface)", "var(--bg)",
	} {
		if strings.Contains(body, usage) {
			t.Errorf("served page still references retired palette token %q — migration to --oc-* is incomplete", usage)
		}
	}
}

// TestTimelineTemplate_DetailViewAndChartDiffUseOptionCTokens verifies the
// two surfaces the PRD calls out by name — the Changeset detail view and the
// Chart diff hunks — now read their ink/muted/line/accent/danger/success
// colors from the --oc-* option-C tokens, so the whole page (shell, detail,
// diff) is one cohesive palette rather than the shell alone.
func TestTimelineTemplate_DetailViewAndChartDiffUseOptionCTokens(t *testing.T) {
	t.Parallel()

	body := servedTimelinePage(t)

	for _, want := range []string{
		// Detail view: panel chrome + the old/new value delta colors.
		`.changeset-detail { border: 1px solid var(--oc-line); border-radius: 10px; background: var(--oc-panel);`,
		`.changeset-detail-header { display: flex; align-items: center; gap: 0.7rem; flex-wrap: wrap; padding-bottom: 0.6rem; border-bottom: 1px solid var(--oc-line-soft);`,
		`.change-old-value, .change-dependency-version-old { font-family: var(--mono); color: var(--oc-danger); }`,
		`.change-new-value, .change-dependency-version-new { font-family: var(--mono); color: var(--oc-success); }`,
		// Chart diff: summary counts + hunk container + line classification.
		`.chart-diff-manifests-changed { font-weight: 600; color: var(--oc-ink); }`,
		`.chart-diff-lines-added { color: var(--oc-success);`,
		`.chart-diff-lines-removed { color: var(--oc-danger);`,
		`.diff-hunks { font-family: var(--mono); font-size: 0.78rem; line-height: 1.5; border: 1px solid var(--oc-line); border-radius: 8px; overflow-x: auto; background: var(--oc-panel);`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("served page missing expected --oc-* rule %q — detail-view/Chart-diff CSS not unified onto the option-C palette", want)
		}
	}
}

// TestTimelineTemplate_FacetAndTimelineChromeUseOptionCTokens verifies the
// facet dropdowns and timeline track controls — the chrome surrounding the
// detail/diff surfaces — are unified onto --oc-* too, so no seam remains
// between "shell" and "everything else" on the same page.
func TestTimelineTemplate_FacetAndTimelineChromeUseOptionCTokens(t *testing.T) {
	t.Parallel()

	body := servedTimelinePage(t)

	for _, want := range []string{
		`.facet-control[data-state="include"] { background: var(--oc-accent); border-color: var(--oc-accent);`,
		`.facet-control[data-state="exclude"] { background: var(--oc-danger); border-color: var(--oc-danger);`,
		`.facet-pill[data-state="include"] { background: var(--oc-accent); border-color: var(--oc-accent);`,
		`.facet-pill[data-state="exclude"] { background: var(--oc-danger); border-color: var(--oc-danger);`,
		`details.facet-dd { border: 1px solid var(--oc-line); border-radius: 8px; background: var(--oc-panel);`,
		`#timeline-root { background: var(--oc-panel); border: 1px solid var(--oc-line);`,
		`.feed-table { width: 100%; border-collapse: collapse; border: 1px solid var(--oc-line); border-radius: 8px; background: var(--oc-panel); }`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("served page missing expected --oc-* rule %q — facet/timeline chrome not unified onto the option-C palette", want)
		}
	}
}
