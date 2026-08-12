package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

// predBase is the reference commit time for predicate-filtered query tests.
var predBase = time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)

// versionCommit builds a one-Change commit bumping a version from old to new,
// committed offset minutes after predBase. Commit SHAs sort in the same order
// as their timestamps, so a test's expected order is readable from the seed.
func versionCommit(sha, old, new string, offset int) domain.Change {
	return domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr(old),
		NewValue:    ptr(new),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   sha,
		Author:      "alice",
		CommittedAt: predBase.Add(time.Duration(offset) * time.Minute),
	}
}

// majorOnly is the predicate under test in most of this file: keep only
// changesets whose rolled-up impact tier is major. It is expressed exactly as
// a caller would express it — the store is handed an opaque func and learns
// nothing about impact classification.
func majorOnly(cs changeset.Changeset) bool {
	return changeset.ClassifyImpact(cs) == changeset.ImpactMajor
}

// shas extracts the commit SHAs of a page, for order-sensitive comparison.
func shas(sets []changeset.Changeset) []string {
	out := make([]string, 0, len(sets))
	for _, cs := range sets {
		out = append(out, cs.CommitSha)
	}
	return out
}

// TestQueryChangesets_PredicateFiltersAssembledChangesets verifies the store
// applies the caller's predicate to fully assembled Changesets and returns
// only survivors, in the same order an unfiltered query would.
func TestQueryChangesets_PredicateFiltersAssembledChangesets(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{
		versionCommit("sha-1-minor", "1.0.0", "1.1.0", 0),
		versionCommit("sha-2-major", "1.1.0", "2.0.0", 1),
		versionCommit("sha-3-patch", "2.0.0", "2.0.1", 2),
		versionCommit("sha-4-major", "2.0.1", "3.0.0", 3),
	})

	w := store.TimeWindow{AsOf: predBase.Add(time.Hour)}

	page, err := s.QueryChangesets(w, filter.FilterSpec{}, majorOnly, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	want := []string{"sha-4-major", "sha-2-major"}
	if got := shas(page.Changesets); !equalStrings(got, want) {
		t.Errorf("filtered SHAs = %v, want %v (newest first)", got, want)
	}
}

// TestQueryChangesets_NilPredicateIsANoOp verifies a nil predicate behaves
// exactly like no filter at all: every caller that does not filter by class
// passes nil, and must see the unfiltered result set unchanged.
func TestQueryChangesets_NilPredicateIsANoOp(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{
		versionCommit("sha-1-minor", "1.0.0", "1.1.0", 0),
		versionCommit("sha-2-major", "1.1.0", "2.0.0", 1),
		versionCommit("sha-3-patch", "2.0.0", "2.0.1", 2),
	})

	w := store.TimeWindow{AsOf: predBase.Add(time.Hour)}

	page, err := s.QueryChangesets(w, filter.FilterSpec{}, nil, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	want := []string{"sha-3-patch", "sha-2-major", "sha-1-minor"}
	if got := shas(page.Changesets); !equalStrings(got, want) {
		t.Errorf("unfiltered SHAs = %v, want %v", got, want)
	}
}

// TestQueryChangesets_FilteredPageIsFullWhileMatchesRemain verifies the page
// is filled to the requested limit even when the predicate rejects most
// commits. A single bounded fetch would return a page roughly 1/k full; the
// store must keep fetching from the seek position until the page is full, the
// result set is exhausted, or the scan budget is spent.
func TestQueryChangesets_FilteredPageIsFullWhileMatchesRemain(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	// 300 commits, every 5th a major bump: 60 matches, far more than one
	// page, with 4 rejects between each pair of matches.
	majors := seedAlternating(t, s, 300, 5)

	w := store.TimeWindow{AsOf: predBase.Add(24 * time.Hour)}

	page := mustPage(t, s, w, filter.FilterSpec{}, majorOnly, "", 10)

	if len(page.Changesets) != 10 {
		t.Fatalf("filtered page has %d changesets, want 10 (a full page while matches remain)", len(page.Changesets))
	}
	if got := shas(page.Changesets); !equalStrings(got, majors[:10]) {
		t.Errorf("filtered page SHAs = %v, want %v", got, majors[:10])
	}
	if page.NextCursor == "" {
		t.Error("NextCursor is empty, but 50 further matches remain")
	}
}

// TestQueryChangesets_FilteredCursorWalkVisitsEveryMatchOnce verifies the
// central pagination contract under filtering: walking with NextCursor until
// it is empty yields every matching changeset exactly once — no gaps, no
// duplicates — in the same order an unfiltered walk visits them.
func TestQueryChangesets_FilteredCursorWalkVisitsEveryMatchOnce(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	majors := seedAlternating(t, s, 300, 5)

	w := store.TimeWindow{AsOf: predBase.Add(24 * time.Hour)}

	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatalf("walk did not terminate after 100 pages; collected %d", len(got))
		}
		page := mustPage(t, s, w, filter.FilterSpec{}, majorOnly, cursor, 7)
		got = append(got, shas(page.Changesets)...)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}

	if !equalStrings(got, majors) {
		t.Errorf("walk yielded %d changesets, want %d; got %v, want %v", len(got), len(majors), got, majors)
	}
}

// TestQueryChangesets_FilteredPagesNeverSplitACommit verifies a commit's
// Changeset is never split across a filtered page boundary: every changeset a
// filtered walk returns carries all of that commit's matching Changes, exactly
// as an unfiltered single-page query would.
func TestQueryChangesets_FilteredPagesNeverSplitACommit(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	// 40 commits, each carrying three Changes, every other one a major bump.
	var seeded []domain.Change
	var wantMajors []string
	for i := range 40 {
		sha := fmt.Sprintf("sha-%04d", i)
		newVal := "1.0.1"
		if i%2 == 0 {
			newVal = "2.0.0"
			wantMajors = append(wantMajors, sha)
		}
		for f := range 3 {
			c := versionCommit(sha, "1.0.0", newVal, i)
			c.Field = fmt.Sprintf("aidp-version-%d", f)
			seeded = append(seeded, c)
		}
	}
	seedChanges(t, s, seeded)

	w := store.TimeWindow{AsOf: predBase.Add(24 * time.Hour)}

	cursor := ""
	seen := make(map[string]int)
	for {
		page := mustPage(t, s, w, filter.FilterSpec{}, majorOnly, cursor, 3)
		for _, cs := range page.Changesets {
			seen[cs.CommitSha]++
			if len(cs.Changes) != 3 {
				t.Errorf("changeset %s has %d Changes, want 3 (commit split across a page boundary)", cs.CommitSha, len(cs.Changes))
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}

	if len(seen) != len(wantMajors) {
		t.Fatalf("walk saw %d distinct commits, want %d", len(seen), len(wantMajors))
	}
	for _, sha := range wantMajors {
		if seen[sha] != 1 {
			t.Errorf("commit %s seen %d times, want exactly 1", sha, seen[sha])
		}
	}
}

// TestQueryChangesets_ScanBudgetBoundsWorkAndStaysResumable verifies the two
// halves of the scan-budget contract against a deliberately pathological
// filter (one that rejects everything) over a result set larger than the
// budget:
//
//  1. The call examines at most MaxChangesetScan commits, so post-assembly
//     filtering never degrades into an unbounded table scan.
//  2. Having spent the budget without filling the page, it still returns a
//     cursor. A short page must never be mistaken for the end of the walk —
//     the absence of a cursor is the only "no more results" signal — and
//     resuming must continue past the commits already examined rather than
//     re-examining them.
//
// It seeds more than MaxChangesetScan commits deliberately: a budget that only
// holds below its own threshold is not a budget.
func TestQueryChangesets_ScanBudgetBoundsWorkAndStaysResumable(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	total := store.MaxChangesetScan + 500
	changes := make([]domain.Change, 0, total)
	for i := range total {
		changes = append(changes, versionCommit(fmt.Sprintf("sha-%06d", i), "1.0.0", "1.0.1", i))
	}
	seedChanges(t, s, changes)

	w := store.TimeWindow{AsOf: predBase.Add(time.Duration(total+1) * time.Minute)}
	rejectAll := func(changeset.Changeset) bool { return false }

	page := mustPage(t, s, w, filter.FilterSpec{}, rejectAll, "", 10)

	if len(page.Changesets) != 0 {
		t.Fatalf("got %d changesets, want 0 (the predicate rejects everything)", len(page.Changesets))
	}
	if page.Examined != store.MaxChangesetScan {
		t.Errorf("Examined = %d, want %d (the budget must be spent, and not exceeded)", page.Examined, store.MaxChangesetScan)
	}
	if page.NextCursor == "" {
		t.Fatal("NextCursor is empty after the budget was spent, but 500 commits remain unexamined")
	}

	// Resuming must pick up past the budget already spent, not restart.
	next := mustPage(t, s, w, filter.FilterSpec{}, rejectAll, page.NextCursor, 10)
	if next.Examined != 500 {
		t.Errorf("resumed call examined %d commits, want exactly the 500 not yet examined", next.Examined)
	}
	if next.NextCursor != "" {
		t.Error("NextCursor is non-empty after the result set was exhausted")
	}
}

// equalStrings compares two string slices element-wise.
func equalStrings(a, b []string) bool {
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

// mustPage runs a query and fails the test on error, keeping the many
// multi-page walks in this file readable.
func mustPage(t *testing.T, s *store.Store, w store.TimeWindow, spec filter.FilterSpec, pred store.ChangesetPredicate, cursor string, limit int) store.ChangesetPage {
	t.Helper()
	page, err := s.QueryChangesets(w, spec, pred, cursor, limit)
	if err != nil {
		t.Fatalf("QueryChangesets(cursor=%q, limit=%d): %v", cursor, limit, err)
	}
	return page
}

// seedAlternating seeds n commits where every kth commit is a major bump and
// the rest are patch bumps, newest last. Returns the SHAs of the major ones,
// newest first — the exact set and order a major-only walk must yield.
func seedAlternating(t *testing.T, s *store.Store, n, k int) []string {
	t.Helper()

	changes := make([]domain.Change, 0, n)
	var majors []string
	for i := range n {
		sha := fmt.Sprintf("sha-%04d", i)
		if i%k == 0 {
			changes = append(changes, versionCommit(sha, "1.0.0", "2.0.0", i))
			majors = append(majors, sha)
			continue
		}
		changes = append(changes, versionCommit(sha, "1.0.0", "1.0.1", i))
	}
	seedChanges(t, s, changes)

	// Reverse into newest-first order, matching query order.
	out := make([]string, 0, len(majors))
	for i := len(majors) - 1; i >= 0; i-- {
		out = append(out, majors[i])
	}
	return out
}
