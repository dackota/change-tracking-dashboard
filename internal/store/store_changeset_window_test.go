package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

// windowChangeAt builds a minimal Change committed at ts, identified by sha.
func windowChangeAt(sha string, ts time.Time) domain.Change {
	return domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		CommitSha:   sha,
		Author:      "alice",
		CommittedAt: ts,
	}
}

// TestQueryChangesets_SinceBoundIsInclusive verifies the lower half of the
// half-open window against real SQL: a Changeset committed at exactly Since
// is returned, and one committed a nanosecond earlier is not. The exact
// boundary is the whole point — an off-by-one-instant here silently drops or
// duplicates a changeset for every polling consumer.
func TestQueryChangesets_SinceBoundIsInclusive(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	since := csBase.Add(time.Hour)
	seedChanges(t, s, []domain.Change{
		windowChangeAt("commit-before", since.Add(-time.Nanosecond)),
		windowChangeAt("commit-at-since", since),
		windowChangeAt("commit-after", since.Add(time.Hour)),
	})

	page, err := s.QueryChangesets(
		store.TimeWindow{Since: since, AsOf: csBase.Add(5 * time.Hour)},
		filter.FilterSpec{}, nil, "", 100,
	)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	got := make(map[string]bool, len(page.Changesets))
	for _, cs := range page.Changesets {
		got[cs.CommitSha] = true
	}

	if !got["commit-at-since"] {
		t.Error("a Changeset committed at exactly Since was excluded, want included")
	}
	if !got["commit-after"] {
		t.Error("a Changeset committed after Since was excluded, want included")
	}
	if got["commit-before"] {
		t.Error("a Changeset committed before Since was included, want excluded")
	}
}

// TestQueryChangesets_AdjacentWindowsTileWithNoGapsOrDuplicates is the
// property the half-open window exists to provide, and the one a polling
// consumer actually depends on: walking the timeline as consecutive windows
// [t0,t1), [t1,t2), [t2,t3) — feeding each window's AsOf straight back as the
// next window's Since — must yield every Changeset exactly once. A closed or
// doubly-open bound would duplicate or drop the changesets sitting exactly on
// a boundary, which is why commits are seeded on the boundaries themselves.
func TestQueryChangesets_AdjacentWindowsTileWithNoGapsOrDuplicates(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	// Boundaries at csBase, +1h, +2h, +3h. Commits land both on the
	// boundaries and between them.
	bounds := []time.Time{csBase, csBase.Add(time.Hour), csBase.Add(2 * time.Hour), csBase.Add(3 * time.Hour)}

	var seeded []domain.Change
	want := make(map[string]bool)
	for i, ts := range []time.Time{
		csBase,                        // exactly on the first boundary
		csBase.Add(30 * time.Minute),  // inside window 1
		csBase.Add(time.Hour),         // exactly on an interior boundary
		csBase.Add(90 * time.Minute),  // inside window 2
		csBase.Add(2 * time.Hour),     // exactly on an interior boundary
		csBase.Add(150 * time.Minute), // inside window 3
	} {
		sha := fmt.Sprintf("commit-%02d", i)
		seeded = append(seeded, windowChangeAt(sha, ts))
		want[sha] = true
	}
	// A commit on the final exclusive boundary must fall outside every window.
	seeded = append(seeded, windowChangeAt("commit-past-end", csBase.Add(3*time.Hour)))
	seedChanges(t, s, seeded)

	seen := make(map[string]int)
	for i := 0; i+1 < len(bounds); i++ {
		w := store.TimeWindow{Since: bounds[i], AsOf: bounds[i+1]}
		page, err := s.QueryChangesets(w, filter.FilterSpec{}, nil, "", 100)
		if err != nil {
			t.Fatalf("QueryChangesets(window %d): %v", i, err)
		}
		for _, cs := range page.Changesets {
			seen[cs.CommitSha]++
		}
	}

	for sha := range want {
		switch seen[sha] {
		case 1: // exactly once, as required
		case 0:
			t.Errorf("%s appeared in no window (gap)", sha)
		default:
			t.Errorf("%s appeared in %d windows, want exactly 1 (duplicate)", sha, seen[sha])
		}
	}
	if n := seen["commit-past-end"]; n != 0 {
		t.Errorf("a commit on the final exclusive boundary appeared in %d windows, want 0", n)
	}
}

// TestQueryChangesets_EmptyWindowReturnsNoChangesetsWithoutError verifies
// that a window whose Since is at or after its AsOf is a normal, empty
// result rather than an error. A polling loop that briefly asks for a
// zero-width or inverted window (clock skew, a poll that fires twice in the
// same instant) should get "nothing happened", not a failure it has to
// special-case.
func TestQueryChangesets_EmptyWindowReturnsNoChangesetsWithoutError(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{
		windowChangeAt("commit-a", csBase),
		windowChangeAt("commit-b", csBase.Add(time.Hour)),
	})

	for _, tc := range []struct {
		name   string
		window store.TimeWindow
	}{
		{"zero width", store.TimeWindow{Since: csBase.Add(time.Hour), AsOf: csBase.Add(time.Hour)}},
		{"inverted", store.TimeWindow{Since: csBase.Add(2 * time.Hour), AsOf: csBase}},
	} {
		page, err := s.QueryChangesets(tc.window, filter.FilterSpec{}, nil, "", 100)
		if err != nil {
			t.Errorf("%s window: unexpected error %v, want an empty result", tc.name, err)
			continue
		}
		if len(page.Changesets) != 0 {
			t.Errorf("%s window: got %d Changesets, want 0", tc.name, len(page.Changesets))
		}
		if page.NextCursor != "" {
			t.Errorf("%s window: got NextCursor %q, want empty (nothing more to walk)", tc.name, page.NextCursor)
		}
	}
}

// TestQueryChangesets_WindowHoldsOnEveryPageOfACursorWalk guards the failure
// mode a single-page test cannot see: a bound applied only when building the
// first page would let a deep cursor walk leak Changesets from outside the
// window. The page size is deliberately smaller than the window's contents,
// so the walk must cross several page boundaries before it reaches the
// window's lower edge.
func TestQueryChangesets_WindowHoldsOnEveryPageOfACursorWalk(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	// 12 commits one hour apart. The window covers indices 3..8 inclusive of
	// 3, exclusive of 9 — six Changesets, walked two at a time.
	var seeded []domain.Change
	for i := range 12 {
		seeded = append(seeded, windowChangeAt(fmt.Sprintf("commit-%02d", i), csBase.Add(time.Duration(i)*time.Hour)))
	}
	seedChanges(t, s, seeded)

	w := store.TimeWindow{Since: csBase.Add(3 * time.Hour), AsOf: csBase.Add(9 * time.Hour)}

	const pageSize = 2
	var walked []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("cursor walk did not terminate")
		}
		page, err := s.QueryChangesets(w, filter.FilterSpec{}, nil, cursor, pageSize)
		if err != nil {
			t.Fatalf("QueryChangesets (cursor=%q): %v", cursor, err)
		}
		for _, cs := range page.Changesets {
			if cs.CommittedAt.Before(w.Since) || !cs.CommittedAt.Before(w.AsOf) {
				t.Errorf("cursor walk returned %s committed at %s, outside the window [%s, %s)",
					cs.CommitSha, cs.CommittedAt, w.Since, w.AsOf)
			}
			walked = append(walked, cs.CommitSha)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	want := []string{"commit-08", "commit-07", "commit-06", "commit-05", "commit-04", "commit-03"}
	if len(walked) != len(want) {
		t.Fatalf("walked %d Changesets %v, want %d %v", len(walked), walked, len(want), want)
	}
	for i, sha := range want {
		if walked[i] != sha {
			t.Errorf("walked[%d] = %q, want %q", i, walked[i], sha)
		}
	}
}
