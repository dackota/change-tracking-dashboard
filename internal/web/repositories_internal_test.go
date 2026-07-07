package web

import (
	"reflect"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// TestBuildRepositoriesView_MapsStatsFieldsToViewRows verifies R3/R4: each
// store.RepositoryStats row maps to a view row carrying its repo, change
// count, chart-change count, and last-change time rendered as both a
// relative phrase and an absolute timestamp — mirroring the timeline KPI
// tile's existing humanizeRelative/formatAbsolute pairing.
func TestBuildRepositoriesView_MapsStatsFieldsToViewRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	stats := []store.RepositoryStats{
		{
			Repo:         "apps-repo",
			ChangeCount:  5,
			ChartChanges: 2,
			LastChangeAt: now.Add(-2 * time.Hour),
		},
	}

	got := buildRepositoriesView(stats, now)

	want := []repositoryView{
		{
			Repo:               "apps-repo",
			ChangeCount:        5,
			ChartChanges:       2,
			LastChangeRelative: "2 hours ago",
			LastChangeAbsolute: formatAbsolute(now.Add(-2 * time.Hour)),
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildRepositoriesView(stats, now) =\n%#v\nwant\n%#v", got, want)
	}
}

// TestBuildRepositoriesView_PreservesStoreOrder verifies the Repositories
// view renders rows in the order store.RepositoryStats returns them (already
// deterministic — R4) rather than re-sorting or reordering them.
func TestBuildRepositoriesView_PreservesStoreOrder(t *testing.T) {
	t.Parallel()

	now := time.Now()
	stats := []store.RepositoryStats{
		{Repo: "alpha-repo", ChangeCount: 1, LastChangeAt: now},
		{Repo: "mid-repo", ChangeCount: 2, LastChangeAt: now},
		{Repo: "zeta-repo", ChangeCount: 3, LastChangeAt: now},
	}

	got := buildRepositoriesView(stats, now)

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	wantOrder := []string{"alpha-repo", "mid-repo", "zeta-repo"}
	for i, w := range wantOrder {
		if got[i].Repo != w {
			t.Errorf("got[%d].Repo = %q, want %q", i, got[i].Repo, w)
		}
	}
}

// TestBuildRepositoriesView_EmptyStats_ReturnsEmptyNotNilSlice verifies the
// degrade-to-empty-state contract (R7): no repositories yields a non-nil,
// zero-length slice, so the template's {{if .Repositories}} branch is driven
// by length alone.
func TestBuildRepositoriesView_EmptyStats_ReturnsEmptyNotNilSlice(t *testing.T) {
	t.Parallel()

	got := buildRepositoriesView([]store.RepositoryStats{}, time.Now())
	if got == nil {
		t.Fatal("buildRepositoriesView([]store.RepositoryStats{}, now) = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// TestBuildRepositoriesView_NilStats_ReturnsEmptyNotNilSlice verifies the
// defensive degrade-to-empty-state contract (R7): a nil stats slice — the
// shape a degraded/failed store read surfaces as at this seam — never
// panics and yields the same empty, non-nil slice as an empty read.
func TestBuildRepositoriesView_NilStats_ReturnsEmptyNotNilSlice(t *testing.T) {
	t.Parallel()

	got := buildRepositoriesView(nil, time.Now())
	if got == nil {
		t.Fatal("buildRepositoriesView(nil, now) = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}
