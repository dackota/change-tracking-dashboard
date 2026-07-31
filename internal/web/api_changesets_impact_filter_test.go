package web_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// impactFilterBase is the reference commit time for impact-filter tests. It is
// in the past so the handler's default asOf ("now") includes every seeded
// commit.
var impactFilterBase = time.Now().Add(-24 * time.Hour)

// seedTieredCommit saves a one-Change commit whose version bump classifies as
// the named tier, with the given repo and facets, offset minutes after
// impactFilterBase.
func seedTieredCommit(t *testing.T, st interface {
	SaveChange(domain.Change) error
}, sha, old, new, repo string, facets map[string]string, offset int) {
	t.Helper()

	c := domain.Change{
		Repo:        repo,
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr(old),
		NewValue:    ptr(new),
		Facets:      facets,
		CommitSha:   sha,
		Author:      "alice",
		CommittedAt: impactFilterBase.Add(time.Duration(offset) * time.Minute),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange(%s): %v", sha, err)
	}
}

// getChangesets issues a GET against the handler and returns the recorder.
func getChangesets(t *testing.T, h http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/changesets"+query, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// changesetSHAs decodes a 200 response and returns the commit SHAs in order.
func changesetSHAs(t *testing.T, rr *httptest.ResponseRecorder) []string {
	t.Helper()

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var body impactChangesetsBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}
	out := make([]string, 0, len(body.Changesets))
	for _, cs := range body.Changesets {
		out = append(out, cs.CommitSha)
	}
	return out
}

// sameSHAs compares two SHA slices element-wise.
func sameSHAs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestChangesetsAPI_ImpactFiltersByTier verifies the impact param selects
// changesets by tier server-side, and that repeated impact params OR together
// rather than the last one winning or the two intersecting to nothing.
func TestChangesetsAPI_ImpactFiltersByTier(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedTieredCommit(t, st, "commit-minor", "1.0.0", "1.1.0", "infra-repo", nil, 0)
	seedTieredCommit(t, st, "commit-major", "1.1.0", "2.0.0", "infra-repo", nil, 1)
	seedTieredCommit(t, st, "commit-patch", "2.0.0", "2.0.1", "infra-repo", nil, 2)
	seedTieredCommit(t, st, "commit-downgrade", "2.0.1", "1.0.0", "infra-repo", nil, 3)

	h := web.NewChangesetsHandler(st)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "single tier",
			query: "?impact=major",
			want:  []string{"commit-major"},
		},
		{
			name:  "repeated params OR together",
			query: "?impact=major&impact=downgrade",
			want:  []string{"commit-downgrade", "commit-major"},
		},
		{
			name:  "every tier is selectable",
			query: "?impact=major&impact=minor&impact=patch&impact=downgrade",
			want:  []string{"commit-downgrade", "commit-patch", "commit-major", "commit-minor"},
		},
		{
			name:  "a tier with no members yields an empty feed",
			query: "?impact=other",
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := changesetSHAs(t, getChangesets(t, h, tt.query))
			if !sameSHAs(got, tt.want) {
				t.Errorf("GET %s returned %v, want %v (newest first)", tt.query, got, tt.want)
			}
		})
	}
}

// TestChangesetsAPI_ImpactUnknownValueIsRejected verifies an unrecognized
// impact value is a 400 rather than being silently ignored. Silently ignoring
// `impact=majr` would return the whole unfiltered feed, which an alerting
// consumer would read as "everything is major" — a worse failure than a
// rejection. The response body must not echo the caller's input back.
func TestChangesetsAPI_ImpactUnknownValueIsRejected(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedTieredCommit(t, st, "commit-major", "1.1.0", "2.0.0", "infra-repo", nil, 0)

	h := web.NewChangesetsHandler(st)

	hostile := "majr<script>"
	tests := []string{
		"?impact=majr",
		"?impact=major&impact=majr",
		"?impact=",
		"?impact=MAJOR",
		"?impact=" + hostile,
	}

	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			t.Parallel()

			rr := getChangesets(t, h, query)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("GET %s status = %d, want 400; body: %s", query, rr.Code, rr.Body.String())
			}
			for _, echoed := range []string{"majr", "MAJOR", hostile, "script"} {
				if strings.Contains(rr.Body.String(), echoed) {
					t.Errorf("GET %s body echoes caller input %q: %s", query, echoed, rr.Body.String())
				}
			}
		})
	}
}

// TestChangesetsAPI_ImpactANDsWithRepoAndFacets verifies impact composes with
// the existing filters by AND, not OR: a changeset must satisfy the impact
// tier and the repo scope and the facet filter to appear.
func TestChangesetsAPI_ImpactANDsWithRepoAndFacets(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	// Two repos x two envs x two tiers, so every combination of the three
	// filters distinguishes a different subset.
	seedTieredCommit(t, st, "infra-dev-major", "1.0.0", "2.0.0", "infra-repo", map[string]string{"env": "dev"}, 0)
	seedTieredCommit(t, st, "infra-prod-major", "1.0.0", "2.0.0", "infra-repo", map[string]string{"env": "prod"}, 1)
	seedTieredCommit(t, st, "infra-prod-patch", "1.0.0", "1.0.1", "infra-repo", map[string]string{"env": "prod"}, 2)
	seedTieredCommit(t, st, "apps-prod-major", "1.0.0", "2.0.0", "apps-repo", map[string]string{"env": "prod"}, 3)

	h := web.NewChangesetsHandler(st)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "impact AND repo",
			query: "?impact=major&repo=infra-repo",
			want:  []string{"infra-prod-major", "infra-dev-major"},
		},
		{
			name:  "impact AND facet",
			query: "?impact=major&env=prod",
			want:  []string{"apps-prod-major", "infra-prod-major"},
		},
		{
			name:  "impact AND repo AND facet",
			query: "?impact=major&repo=infra-repo&env=prod",
			want:  []string{"infra-prod-major"},
		},
		{
			name:  "impact excludes a changeset the other filters admit",
			query: "?impact=patch&repo=infra-repo&env=prod",
			want:  []string{"infra-prod-patch"},
		},
		{
			name:  "no overlap yields an empty feed",
			query: "?impact=patch&repo=apps-repo",
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := changesetSHAs(t, getChangesets(t, h, tt.query))
			if !sameSHAs(got, tt.want) {
				t.Errorf("GET %s returned %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

// TestChangesetsAPI_ImpactOmittedIsUnchanged verifies omitting impact leaves
// the response byte-identical to what it was before the param existed —
// existing clients are unaffected.
func TestChangesetsAPI_ImpactOmittedIsUnchanged(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for i, tier := range [][2]string{{"1.0.0", "2.0.0"}, {"1.0.0", "1.1.0"}, {"1.0.0", "1.0.1"}} {
		seedTieredCommit(t, st, fmt.Sprintf("commit-%d", i), tier[0], tier[1], "infra-repo", nil, i)
	}

	h := web.NewChangesetsHandler(st)

	asOf := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	bare := getChangesets(t, h, "?asOf="+asOf)
	allTiers := getChangesets(t, h, "?asOf="+asOf+"&impact=major&impact=minor&impact=patch&impact=downgrade&impact=other")

	if bare.Code != http.StatusOK {
		t.Fatalf("bare request status = %d, want 200; body: %s", bare.Code, bare.Body.String())
	}
	if bare.Body.String() != allTiers.Body.String() {
		t.Errorf("omitting impact differs from selecting every tier\n omitted: %s\n all:     %s", bare.Body.String(), allTiers.Body.String())
	}
	if got := changesetSHAs(t, bare); len(got) != 3 {
		t.Errorf("omitting impact returned %d changesets, want all 3", len(got))
	}
}

// TestChangesetsAPI_ImpactIsReservedNotAFacet verifies impact is never treated
// as a facet filter, even when the stored data happens to carry a facet named
// "impact". Without the reservation, a repo whose paths yielded such a facet
// would silently change the meaning of the param.
func TestChangesetsAPI_ImpactIsReservedNotAFacet(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	// A commit carrying a literal "impact" facet whose value is a valid tier
	// name, but whose classified tier is something else entirely.
	seedTieredCommit(t, st, "commit-patch-tagged-major", "1.0.0", "1.0.1", "infra-repo", map[string]string{"impact": "major"}, 0)
	seedTieredCommit(t, st, "commit-真-major", "1.0.0", "2.0.0", "infra-repo", nil, 1)

	h := web.NewChangesetsHandler(st)

	// impact=major must select by classified tier, not by the facet value.
	got := changesetSHAs(t, getChangesets(t, h, "?impact=major"))
	want := []string{"commit-真-major"}
	if !sameSHAs(got, want) {
		t.Errorf("GET ?impact=major returned %v, want %v (impact is reserved, never a facet)", got, want)
	}
}
