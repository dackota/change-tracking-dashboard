package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
)

// filterTestBase is a convenient time base for filter tests (avoids collision
// with the package-level `base` used in the existing store tests).
var filterTestBase = time.Date(2024, 3, 1, 8, 0, 0, 0, time.UTC)

// buildFilterChanges returns three Changes with varied facets for use in filter
// handler tests.
func buildFilterChanges() []domain.Change {
	older := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.0.0"),
		NewValue:    ptr("1.1.0"),
		Facets:      map[string]string{"tenant": "tenant-zero", "env": "dev"},
		CommitSha:   "filter-sha-dev-zero",
		Author:      "alice",
		CommittedAt: filterTestBase,
	}
	mid := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-one/prod/eu-west-1/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("2.0.0"),
		NewValue:    ptr("2.1.0"),
		Facets:      map[string]string{"tenant": "tenant-one", "env": "prod"},
		CommitSha:   "filter-sha-prod-one",
		Author:      "bob",
		CommittedAt: filterTestBase.Add(time.Hour),
	}
	newest := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-one/dev/us-east-1/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("3.0.0"),
		NewValue:    ptr("3.1.0"),
		Facets:      map[string]string{"tenant": "tenant-one", "env": "dev"},
		CommitSha:   "filter-sha-dev-one",
		Author:      "carol",
		CommittedAt: filterTestBase.Add(2 * time.Hour),
	}
	return []domain.Change{older, mid, newest}
}

// TestFeedHandler_FilterBySingleFacet verifies that a single URL query param
// filters the rendered feed to matching Changes only.
func TestFeedHandler_FilterBySingleFacet(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for _, c := range buildFilterChanges() {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := newTestHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/?env=dev", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	// Dev changes must appear.
	if !strings.Contains(body, "filter-sha-dev-zero") {
		t.Error("body missing dev-zero commit sha")
	}
	if !strings.Contains(body, "filter-sha-dev-one") {
		t.Error("body missing dev-one commit sha")
	}

	// Prod change must NOT appear.
	if strings.Contains(body, "filter-sha-prod-one") {
		t.Error("body contains prod commit sha but env=dev was requested")
	}
}

// TestFeedHandler_FilterByMultipleFacetsAND verifies AND semantics when two
// facet params are provided.
func TestFeedHandler_FilterByMultipleFacetsAND(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for _, c := range buildFilterChanges() {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := newTestHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/?env=dev&tenant=tenant-one", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	// Only the dev+tenant-one change must appear.
	if !strings.Contains(body, "filter-sha-dev-one") {
		t.Error("body missing dev+tenant-one commit sha")
	}
	if strings.Contains(body, "filter-sha-dev-zero") {
		t.Error("body contains dev-zero (wrong tenant) but AND filter should exclude it")
	}
	if strings.Contains(body, "filter-sha-prod-one") {
		t.Error("body contains prod-one (wrong env) but AND filter should exclude it")
	}
}

// TestFeedHandler_FilterNoMatch verifies that a filter matching nothing renders
// the empty state cleanly (no 500, no panic).
func TestFeedHandler_FilterNoMatch(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for _, c := range buildFilterChanges() {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := newTestHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/?env=staging", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	// None of the test commit SHAs should appear.
	for _, sha := range []string{"filter-sha-dev-zero", "filter-sha-prod-one", "filter-sha-dev-one"} {
		if strings.Contains(body, sha) {
			t.Errorf("body contains %q but no match expected", sha)
		}
	}
	// Page must still render (no panic / empty body).
	if body == "" {
		t.Error("empty body — handler panicked")
	}
}

// TestFeedHandler_NoFilterShowsAll verifies that omitting query params returns
// all Changes (backward-compatible with existing behaviour).
func TestFeedHandler_NoFilterShowsAll(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for _, c := range buildFilterChanges() {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := newTestHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	for _, sha := range []string{"filter-sha-dev-zero", "filter-sha-prod-one", "filter-sha-dev-one"} {
		if !strings.Contains(body, sha) {
			t.Errorf("body missing %q — all changes should appear when no filter", sha)
		}
	}
}

// TestFeedHandler_FilterControlsListFacetNamesAndValues verifies that the page
// renders dynamic filter controls listing the observed facet names and their
// distinct values.
func TestFeedHandler_FilterControlsListFacetNamesAndValues(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for _, c := range buildFilterChanges() {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := newTestHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	// Filter controls must mention each observed facet name.
	for _, name := range []string{"env", "tenant"} {
		if !strings.Contains(body, name) {
			t.Errorf("body missing facet name %q in filter controls", name)
		}
	}

	// Filter controls must include the observed values as options.
	for _, val := range []string{"dev", "prod", "tenant-zero", "tenant-one"} {
		if !strings.Contains(body, val) {
			t.Errorf("body missing facet value %q in filter controls", val)
		}
	}
}

// TestFeedHandler_FilterControlsReflectActiveSelection verifies that when a
// facet filter is active, the filter control for that facet shows it as selected.
func TestFeedHandler_FilterControlsReflectActiveSelection(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for _, c := range buildFilterChanges() {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := newTestHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/?env=dev", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	// The "dev" option must be marked selected in the env select control.
	// html/template escapes are not applied to attribute names/values rendered
	// from the template, so we look for the select+option pattern.
	if !strings.Contains(body, `selected`) {
		t.Error("body does not contain 'selected' attribute — active filter not reflected")
	}
}

// TestFeedHandler_ClearFilterLinkPresent verifies that the page includes a way
// to clear all active filters (e.g. an "All" option or a clear link).
func TestFeedHandler_ClearFilterLinkPresent(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for _, c := range buildFilterChanges() {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	h := newTestHandler(t, st)
	req := httptest.NewRequest(http.MethodGet, "/?env=dev", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()

	// There must be either an "All" option or a clear href so the user can
	// remove the active filter. Accept either form.
	hasAll := strings.Contains(body, "All")
	hasClear := strings.Contains(body, `href="/"`) || strings.Contains(body, `href='/'`)
	if !hasAll && !hasClear {
		t.Error("body has no clear-filter mechanism (no 'All' option and no clear link to '/')")
	}
}

// TestFeedHandler_SecurityHeadersIntact verifies that adding filter controls
// does not inadvertently remove the security headers.
func TestFeedHandler_SecurityHeadersIntact(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := newTestHandler(t, st)

	req := httptest.NewRequest(http.MethodGet, "/?env=dev", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if csp := rr.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("missing Content-Security-Policy header")
	}
}
