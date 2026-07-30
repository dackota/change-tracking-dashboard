package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// getChangesetsWithParams issues GET /api/changesets with the given query
// params against a handler backed by st, returning the recorder.
func getChangesetsWithParams(t *testing.T, h http.Handler, params url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/changesets?"+params.Encode(), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestChangesetsAPI_Since_ExcludesOlderChangesets verifies the endpoint
// honors the since lower bound end-to-end: a Changeset committed before
// since is absent from the response, one committed after it is present.
// This is the request an incremental consumer makes on every poll, so it
// must narrow the feed rather than being silently ignored.
func TestChangesetsAPI_Since_ExcludesOlderChangesets(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	seedChange(t, st, changeSpec{CommitSha: "old-commit", Age: 48 * time.Hour})
	seedChange(t, st, changeSpec{CommitSha: "recent-commit", Age: time.Hour})

	params := url.Values{}
	params.Set("since", time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339))

	rr := getChangesetsWithParams(t, h, params)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	got := make(map[string]bool, len(body.Changesets))
	for _, cs := range body.Changesets {
		got[cs.CommitSha] = true
	}

	if !got["recent-commit"] {
		t.Error("a Changeset committed after since is missing, want it returned")
	}
	if got["old-commit"] {
		t.Error("a Changeset committed before since was returned, want it excluded")
	}
}

// TestChangesetsAPI_Since_MalformedIsRejected verifies a bad since fails
// loudly with 400 rather than being silently ignored. Silently ignoring it
// would return the entire history to a consumer that believes it asked for a
// narrow window — the failure mode most likely to go unnoticed in production.
// The response must not echo the caller's input back.
func TestChangesetsAPI_Since_MalformedIsRejected(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	for _, raw := range []string{"not-a-timestamp", "2024-13-45", "1700000000", "<script>"} {
		params := url.Values{}
		params.Set("since", raw)

		rr := getChangesetsWithParams(t, h, params)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("since=%q: status = %d, want 400", raw, rr.Code)
			continue
		}
		if strings.Contains(rr.Body.String(), raw) {
			t.Errorf("since=%q: response body echoed the caller's input: %s", raw, rr.Body.String())
		}
	}
}

// TestChangesetsAPI_Since_AbsentReturnsFullHistory verifies the
// backward-compatibility guarantee: with no since param the endpoint behaves
// exactly as it always has, returning Changesets of any age. Every existing
// client depends on this.
func TestChangesetsAPI_Since_AbsentReturnsFullHistory(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	seedChange(t, st, changeSpec{CommitSha: "ancient-commit", Age: 10000 * time.Hour})
	seedChange(t, st, changeSpec{CommitSha: "recent-commit", Age: time.Hour})

	rr := getChangesetsWithParams(t, h, url.Values{})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	got := make(map[string]bool, len(body.Changesets))
	for _, cs := range body.Changesets {
		got[cs.CommitSha] = true
	}

	for _, sha := range []string{"ancient-commit", "recent-commit"} {
		if !got[sha] {
			t.Errorf("%s missing with no since param, want the full history returned", sha)
		}
	}
}

// TestChangesetsAPI_Since_IsReservedAgainstASameNamedFacet verifies that a
// configured facet literally named "since" can never hijack the time bound.
// Facet names come from operator config and are matched against query-param
// keys, so without a reservation the two would collide and the window would
// silently stop being applied.
func TestChangesetsAPI_Since_IsReservedAgainstASameNamedFacet(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	// A Change carrying a facet actually named "since".
	seedChangeWithFacets(t, st, "faceted-commit", map[string]string{"since": "whenever"})

	params := url.Values{}
	params.Set("since", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))

	rr := getChangesetsWithParams(t, h, params)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// since is in the future, so it must be read as a time bound (excluding
	// the Changeset) and never as a facet filter (which would have matched
	// the value "whenever" — or, failing to match it, excluded it for the
	// wrong reason and hidden the bug).
	body := decodeChangesetsBody(t, rr)
	for _, cs := range body.Changesets {
		if cs.CommitSha == "faceted-commit" {
			t.Error("since was treated as a facet filter rather than a time bound")
		}
	}
}
