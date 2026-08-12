package web_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// walkChangesets follows nextCursor from the given query until it comes back
// empty, returning every commit SHA visited in order along with the number of
// pages. It fails the test if the walk does not terminate promptly, which is
// the failure mode a broken cursor produces.
func walkChangesets(t *testing.T, h http.Handler, query string, limit int) ([]string, int) {
	t.Helper()

	var got []string
	cursor := ""
	for pages := 1; ; pages++ {
		if pages > 200 {
			t.Fatalf("walk did not terminate after 200 pages; collected %d", len(got))
		}
		q := fmt.Sprintf("%s&limit=%d", query, limit)
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		rr := getChangesets(t, h, "?"+q)
		got = append(got, changesetSHAs(t, rr)...)

		var body struct {
			NextCursor string `json:"nextCursor"`
		}
		decodeInto(t, rr, &body)
		cursor = body.NextCursor
		if cursor == "" {
			return got, pages
		}
	}
}

// TestChangesetsAPI_RiskFilteredPaginationIsCorrect verifies the pagination
// guarantees established for impact in #148 hold for risk as well: pages are
// filled to the requested limit while matches remain (rather than arriving
// pre-punctured by rejections), and a full cursor walk visits every matching
// changeset exactly once in newest-first order.
//
// This matters because risk is the second predicate to ride the page-fill
// loop. If the loop had quietly depended on something specific to impact —
// say, that every changeset carries exactly one class — risk would be where
// that assumption broke.
func TestChangesetsAPI_RiskFilteredPaginationIsCorrect(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	// 90 commits, every third carrying a security risk: 30 matches separated
	// by two non-matching commits each, so an unfilled page would be obvious.
	var wantMatches []string
	for i := range 90 {
		sha := fmt.Sprintf("commit-%03d", i)
		if i%3 == 0 {
			seedRiskCommit(t, st, sha, "oci-vcn-security-list.tf", "source", "10.0.0.0/8", "0.0.0.0/0", "infra-repo", nil, i)
			wantMatches = append(wantMatches, sha)
			continue
		}
		seedRiskCommit(t, st, sha, "oci-vcn.tf", "vcn-display-name", "old-name", fmt.Sprintf("name-%d", i), "infra-repo", nil, i)
	}
	// Newest-first is the reverse of seed order.
	for i, j := 0, len(wantMatches)-1; i < j; i, j = i+1, j-1 {
		wantMatches[i], wantMatches[j] = wantMatches[j], wantMatches[i]
	}

	h := web.NewChangesetsHandler(st)

	// A filtered page is as full as an unfiltered one while matches remain.
	first := changesetSHAs(t, getChangesets(t, h, "?risk=security&limit=10"))
	if len(first) != 10 {
		t.Errorf("first filtered page has %d changesets, want a full 10 (20 further matches remain)", len(first))
	}

	// The full walk visits every match exactly once, in order.
	got, pages := walkChangesets(t, h, "risk=security", 7)
	if !sameSHAs(got, wantMatches) {
		t.Errorf("filtered walk over %d pages yielded %d changesets, want %d\n got: %v\nwant: %v",
			pages, len(got), len(wantMatches), got, wantMatches)
	}

	seen := make(map[string]int, len(got))
	for _, sha := range got {
		seen[sha]++
	}
	for sha, n := range seen {
		if n != 1 {
			t.Errorf("commit %s visited %d times, want exactly 1", sha, n)
		}
	}
}

// decodeInto unmarshals a 200 response body into v.
func decodeInto(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()

	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}
}
