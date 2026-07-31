package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// seedDetailChangeset saves a two-Change commit and returns a handler serving
// it, along with the query string that addresses it.
func seedDetailChangeset(t *testing.T) (*web.ChangesetDetailHandler, string) {
	t.Helper()

	st := newTestStore(t)
	base := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.9.0"),
		NewValue:    ptr("2.0.0"),
		CommitSha:   "commit-detail",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	second := base
	second.Field = "sidecarTag"
	second.OldValue = ptr("3.1.0")
	second.NewValue = ptr("3.1.1")

	for _, c := range []domain.Change{base, second} {
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	return web.NewChangesetDetailHandler(st), "?repo=infra-repo&commitSha=commit-detail"
}

// getDetail issues a GET against the detail handler with the given Accept
// header. An empty accept sends no Accept header at all — the distinction
// matters, since an absent header and "*/*" are different requests even though
// both must yield HTML.
func getDetail(t *testing.T, h http.Handler, query, accept string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail"+query, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestChangesetDetail_ExplicitJSONAcceptReturnsJSON verifies the negotiation
// rule's positive half: an Accept header that explicitly names
// application/json gets a JSON body with a matching Content-Type.
func TestChangesetDetail_ExplicitJSONAcceptReturnsJSON(t *testing.T) {
	t.Parallel()

	h, query := seedDetailChangeset(t)

	accepts := []string{
		"application/json",
		"application/json, text/html",
		"text/html, application/json",
		"application/json;q=0.9",
		"application/json, */*",
		"Application/JSON",
	}

	for _, accept := range accepts {
		t.Run(accept, func(t *testing.T) {
			t.Parallel()

			rr := getDetail(t, h, query, accept)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
			}
			if body["commitSha"] != "commit-detail" {
				t.Errorf("commitSha = %v, want commit-detail; body: %s", body["commitSha"], rr.Body.String())
			}
		})
	}
}

// TestChangesetDetail_JSONShapeMatchesListEndpoint verifies the detail JSON is
// the same changeset shape the list endpoint emits — same field names, same
// computed risk[] and impact projections — rather than a detail-specific
// variant. A client that already parses the feed must not need a second type
// to parse a single changeset.
func TestChangesetDetail_JSONShapeMatchesListEndpoint(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.9.0"),
		NewValue:    ptr("2.0.0"),
		CommitSha:   "commit-shape",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	// The same changeset as the list endpoint renders it...
	listRR := getChangesets(t, web.NewChangesetsHandler(st), "")
	var listBody struct {
		Changesets []json.RawMessage `json:"changesets"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("unmarshal list body: %v", err)
	}
	if len(listBody.Changesets) != 1 {
		t.Fatalf("list returned %d changesets, want 1", len(listBody.Changesets))
	}

	// ...must be byte-identical to the detail endpoint's JSON for it.
	detailRR := getDetail(t, web.NewChangesetDetailHandler(st), "?repo=infra-repo&commitSha=commit-shape", "application/json")
	if detailRR.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200; body: %s", detailRR.Code, detailRR.Body.String())
	}

	if got, want := detailRR.Body.String(), string(listBody.Changesets[0]); got != want {
		t.Errorf("detail JSON differs from the list endpoint's element for the same changeset\n detail: %s\n list:   %s", got, want)
	}

	// Spot-check the computed projections are actually present and populated,
	// so an identical-but-empty shape cannot pass the comparison above.
	var detail map[string]any
	if err := json.Unmarshal(detailRR.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal detail body: %v", err)
	}
	if detail["impact"] != "major" {
		t.Errorf("impact = %v, want major", detail["impact"])
	}
	if _, ok := detail["risk"]; !ok {
		t.Error(`detail JSON has no "risk" field`)
	}
	if changes, ok := detail["changes"].([]any); !ok || len(changes) != 1 {
		t.Errorf("changes = %v, want 1 element", detail["changes"])
	}
}

// TestChangesetDetail_NonJSONAcceptReturnsHTML is the live-UI regression
// guard, and the reason the negotiation rule requires an *explicit* mention of
// application/json.
//
// The dashboard's own timeline.js fetches this endpoint with XMLHttpRequest
// and never sets an Accept header, so the browser sends "*/*". Treating a
// wildcard as opting into JSON would silently replace the rendered HTML
// fragments the UI splices into the page with a JSON document — breaking the
// live UI while every API test still passed. Each case below must return
// today's HTML, byte-for-byte identical to what an unmodified handler returns.
func TestChangesetDetail_NonJSONAcceptReturnsHTML(t *testing.T) {
	t.Parallel()

	h, query := seedDetailChangeset(t)

	// The baseline: no Accept header at all, which is what the handler saw
	// before negotiation existed.
	baseline := getDetail(t, h, query, "")
	if baseline.Code != http.StatusOK {
		t.Fatalf("baseline status = %d, want 200; body: %s", baseline.Code, baseline.Body.String())
	}

	accepts := []struct {
		name   string
		accept string
	}{
		{"absent header", ""},
		{"wildcard from XMLHttpRequest", "*/*"},
		{"html", "text/html"},
		{"html with wildcard", "text/html,application/xhtml+xml,*/*;q=0.8"},
		{"a type-level wildcard is not an explicit mention", "application/*"},
		{"an unrelated type", "application/xml"},
	}

	for _, tt := range accepts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := getDetail(t, h, query, tt.accept)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html...", ct)
			}
			if rr.Body.String() != baseline.Body.String() {
				t.Errorf("Accept %q changed the HTML body\n got: %s\nwant: %s", tt.accept, rr.Body.String(), baseline.Body.String())
			}
			if strings.HasPrefix(strings.TrimSpace(rr.Body.String()), "{") {
				t.Errorf("Accept %q returned what looks like JSON: %s", tt.accept, rr.Body.String())
			}
		})
	}
}
