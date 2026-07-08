package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// TestChangesetsHandler_QueryChangesets_WrappedInSpan verifies criterion 5
// for the HTTP seam: GET /api/changesets' downstream store.QueryChangesets
// call is wrapped in its own span. This test deliberately does NOT run in
// parallel with the package's many t.Parallel() tests: it reads from the
// process-wide spanExporter TestMain installs as the global TracerProvider
// exactly once for the whole binary (see main_test.go's doc for why that
// must be centralized rather than each span-assertion test installing its
// own), resetting it first to isolate this assertion from any earlier test's
// spans. Go's test runner fully drains all non-parallel tests before any
// t.Parallel() test resumes, so no concurrently-running sibling can write to
// spanExporter between the Reset and the assertion below.
func TestChangesetsHandler_QueryChangesets_WrappedInSpan(t *testing.T) {
	spanExporter.Reset()

	st := newTestStore(t)
	h := web.NewChangesetsHandler(st)

	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	found := false
	for _, s := range spanExporter.GetSpans() {
		if s.Name == "store.query_changesets" {
			found = true
		}
	}
	if !found {
		t.Errorf("no span named store.query_changesets recorded; got spans: %v", spanExporter.GetSpans())
	}
}
