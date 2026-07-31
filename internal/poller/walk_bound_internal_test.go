// Package poller (internal test): the walk bound a tracker group's single
// shared walk runs with, and the guarantee that bounding it never costs a
// field history it would otherwise have seen (#180).
//
// Before #180 the bound was surrendered — set to the zero time, meaning walk
// to the repo root — as soon as any field had a cursor, which in the steady
// state is every field on every cycle. Now a cursor carries its own timestamp,
// so the group can bound the walk at the oldest thing anyone still needs.
//
// The risk this file exists to pin: the walk breaks on AUTHOR time but
// proceeds in COMMITTER order, so a bounded walk can stop early and miss a
// cursor the bound was computed to include. pollFileGroup detects that and
// re-walks unbounded, which is what keeps the optimization from changing what
// any field records.
package poller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// boundTestTracker is a tracker with the given backfill window.
func boundTestTracker(backfillDays int) domain.Tracker {
	return domain.Tracker{
		Repo: "r", FileGlob: "f", Field: "x",
		ExtractorExpr: ".version", BackfillDays: backfillDays,
	}
}

// boundTestPoller returns a Poller whose clock is pinned to now.
func boundTestPoller(now time.Time) *Poller {
	return &Poller{now: func() time.Time { return now }}
}

// TestGroupWalkBound_CursorTimeBoundsTheWalk is the change #180 is about: a
// field with a timestamped cursor bounds the walk at that cursor rather than
// dropping the bound and walking to the root.
func TestGroupWalkBound_CursorTimeBoundsTheWalk(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	cursorAt := now.Add(-2 * time.Hour)

	p := boundTestPoller(now)
	members := []groupMember{{tracker: boundTestTracker(90)}}
	hwms := []store.HighWaterMark{{Sha: "abc", CommittedAt: cursorAt}}

	got := p.groupWalkBound(members, hwms, []int{0})
	if !got.Equal(cursorAt) {
		t.Errorf("groupWalkBound = %v, want the cursor's own time %v", got, cursorAt)
	}
}

// TestGroupWalkBound_TakesTheOldestBoundaryAnyFieldNeeds covers the union rule
// the shared walk depends on: with a mix of cursored and first-run fields, the
// bound must be the oldest of every field's own boundary, or some field's
// range would be missing from the one walk they share.
func TestGroupWalkBound_TakesTheOldestBoundaryAnyFieldNeeds(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	p := boundTestPoller(now)

	recentCursor := now.Add(-1 * time.Hour)
	oldCursor := now.AddDate(0, 0, -200)

	tests := []struct {
		name    string
		members []groupMember
		hwms    []store.HighWaterMark
		want    time.Time
	}{
		{
			name:    "two cursors: the older one wins",
			members: []groupMember{{tracker: boundTestTracker(90)}, {tracker: boundTestTracker(90)}},
			hwms: []store.HighWaterMark{
				{Sha: "a", CommittedAt: recentCursor},
				{Sha: "b", CommittedAt: oldCursor},
			},
			want: oldCursor,
		},
		{
			name:    "a first-run field's backfill window is older than the other's cursor",
			members: []groupMember{{tracker: boundTestTracker(90)}, {tracker: boundTestTracker(90)}},
			hwms: []store.HighWaterMark{
				{Sha: "a", CommittedAt: recentCursor},
				{}, // first run
			},
			want: now.AddDate(0, 0, -90),
		},
		{
			name:    "a cursor older than the other field's backfill window",
			members: []groupMember{{tracker: boundTestTracker(30)}, {tracker: boundTestTracker(30)}},
			hwms: []store.HighWaterMark{
				{}, // first run: 30 days back
				{Sha: "b", CommittedAt: oldCursor},
			},
			want: oldCursor,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := p.groupWalkBound(tc.members, tc.hwms, []int{0, 1})
			if !got.Equal(tc.want) {
				t.Errorf("groupWalkBound = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGroupWalkBound_UnboundedWins covers every case that must still surrender
// the bound. Getting any of these wrong truncates a walk below what some field
// needs, which silently loses history.
func TestGroupWalkBound_UnboundedWins(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	p := boundTestPoller(now)

	tests := []struct {
		name    string
		members []groupMember
		hwms    []store.HighWaterMark
	}{
		{
			name:    "a cursor with no recorded time (a row predating #180)",
			members: []groupMember{{tracker: boundTestTracker(90)}, {tracker: boundTestTracker(90)}},
			hwms: []store.HighWaterMark{
				{Sha: "a", CommittedAt: now.Add(-time.Hour)},
				{Sha: "b"}, // legacy row: sha known, time unknown
			},
		},
		{
			name:    "a first-run field with an unbounded backfill window",
			members: []groupMember{{tracker: boundTestTracker(-1)}, {tracker: boundTestTracker(90)}},
			hwms: []store.HighWaterMark{
				{},
				{Sha: "b", CommittedAt: now.Add(-time.Hour)},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := p.groupWalkBound(tc.members, tc.hwms, []int{0, 1}); !got.IsZero() {
				t.Errorf("groupWalkBound = %v, want the zero time (unbounded)", got)
			}
		})
	}
}

// TestCursorsPresent covers the check that makes bounding safe: it must accept
// a history containing every cursor, reject one missing any, and ignore
// first-run fields, which have no cursor to find.
func TestCursorsPresent(t *testing.T) {
	t.Parallel()

	history := gitsource.History{
		{CommitSha: "c1"}, {CommitSha: "c2"}, {CommitSha: "c3"},
	}

	tests := []struct {
		name string
		hwms []store.HighWaterMark
		want bool
	}{
		{name: "every cursor present", hwms: []store.HighWaterMark{{Sha: "c1"}, {Sha: "c3"}}, want: true},
		{name: "one cursor missing", hwms: []store.HighWaterMark{{Sha: "c1"}, {Sha: "gone"}}, want: false},
		{name: "first-run fields have no cursor to find", hwms: []store.HighWaterMark{{}, {}}, want: true},
		{name: "mixed first-run and present cursor", hwms: []store.HighWaterMark{{}, {Sha: "c2"}}, want: true},
		{name: "mixed first-run and missing cursor", hwms: []store.HighWaterMark{{}, {Sha: "gone"}}, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := cursorsPresent(history, tc.hwms, []int{0, 1}); got != tc.want {
				t.Errorf("cursorsPresent = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCursorsPresent_EmptyHistory verifies the degenerate case: no history
// contains no cursors, so any cursored field must fail the check rather than
// pass vacuously.
func TestCursorsPresent_EmptyHistory(t *testing.T) {
	t.Parallel()

	if cursorsPresent(gitsource.History{}, []store.HighWaterMark{{Sha: "c1"}}, []int{0}) {
		t.Error("cursorsPresent on an empty history with a cursored field = true, want false")
	}
	if !cursorsPresent(gitsource.History{}, []store.HighWaterMark{{}}, []int{0}) {
		t.Error("cursorsPresent on an empty history with only first-run fields = false, want true")
	}
}

// buildSkewedRepo commits one file with monotonically increasing COMMITTER
// times (which fix walk order) and the given AUTHOR day offsets (which the
// bound is compared against). It returns the repo path and each commit's SHA
// in commit order.
func buildSkewedRepo(t *testing.T, authorDays []int) (repoPath string, shas []string) {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	for i, d := range authorDays {
		if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(fmt.Sprintf("version: \"1.%d.0\"\n", i)), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add("Chart.yaml"); err != nil {
			t.Fatalf("git add: %v", err)
		}
		h, err := wt.Commit(fmt.Sprintf("c%d", i), &git.CommitOptions{
			Author:    &object.Signature{Name: "dev", Email: "d@x.com", When: base.AddDate(0, 0, d)},
			Committer: &object.Signature{Name: "ci", Email: "c@x.com", When: base.AddDate(0, 0, 100+i)},
		})
		if err != nil {
			t.Fatalf("commit c%d: %v", i, err)
		}
		shas = append(shas, h.String())
	}
	return dir, shas
}

// TestBoundedWalk_NeverHidesACursor_EvenUnderDateSkew is the property the
// whole optimization rests on, asserted the only way that is actually safe:
// for EVERY commit in a date-skewed history taken as the cursor, bounding the
// walk at that cursor's own timestamp must still yield a history containing
// it — or, when it does not, pollFileGroup's fallback must be what saves it.
//
// This is not hypothetical. Author time is not monotonic in walk order, so a
// commit newer in the walk but older in author time truncates the walk before
// it reaches the cursor. The subtests that report a miss below are the cases
// that would have silently lost a field's diff baseline had the bound been
// trusted rather than checked.
func TestBoundedWalk_NeverHidesACursor_EvenUnderDateSkew(t *testing.T) {
	t.Parallel()

	repoPath, shas := buildSkewedRepo(t, []int{0, 9, 3, 12, 1, 7, 5, 20, 2, 15})
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	ctx := context.Background()

	full, err := src.WalkCommits(ctx, "Chart.yaml", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits (full): %v", err)
	}

	byS1ha := make(map[string]domain.CommitSnapshot, len(full))
	for _, snap := range full {
		byS1ha[snap.CommitSha] = snap
	}

	var missed int
	for i, sha := range shas {
		snap, ok := byS1ha[sha]
		if !ok {
			t.Fatalf("commit %d (%s) missing from the unbounded walk", i, sha)
		}

		// Bound exactly as groupWalkBound would for a single cursored field.
		bounded, err := src.WalkCommits(ctx, "Chart.yaml", snap.CommittedAt)
		if err != nil {
			t.Fatalf("WalkCommits (bounded at commit %d): %v", i, err)
		}

		hwms := []store.HighWaterMark{{Sha: sha, CommittedAt: snap.CommittedAt}}
		if !cursorsPresent(bounded, hwms, []int{0}) {
			// The bound fell short. The unbounded re-walk pollFileGroup falls
			// back to must recover it — that fallback is the guarantee.
			missed++
			if !cursorsPresent(full, hwms, []int{0}) {
				t.Errorf("commit %d (%s): cursor missing from the bounded walk AND from the unbounded fallback",
					i, sha)
			}
		}
	}

	t.Logf("%d of %d cursors needed the unbounded fallback under date skew", missed, len(shas))
}

// TestBoundedWalk_MonotonicDates_NeedsNoFallback is the other half: with
// well-behaved dates — the common case in a real repo — bounding at the
// cursor's timestamp always reaches it, so the fallback never fires and the
// walk really is bounded. Without this, the property above could be satisfied
// by a bound that never works and always falls back.
func TestBoundedWalk_MonotonicDates_NeedsNoFallback(t *testing.T) {
	t.Parallel()

	repoPath, shas := buildSkewedRepo(t, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	ctx := context.Background()

	full, err := src.WalkCommits(ctx, "Chart.yaml", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits (full): %v", err)
	}
	bySha := make(map[string]domain.CommitSnapshot, len(full))
	for _, snap := range full {
		bySha[snap.CommitSha] = snap
	}

	for i, sha := range shas {
		snap := bySha[sha]
		bounded, err := src.WalkCommits(ctx, "Chart.yaml", snap.CommittedAt)
		if err != nil {
			t.Fatalf("WalkCommits (bounded at commit %d): %v", i, err)
		}

		hwms := []store.HighWaterMark{{Sha: sha, CommittedAt: snap.CommittedAt}}
		if !cursorsPresent(bounded, hwms, []int{0}) {
			t.Errorf("commit %d (%s): bounded walk missed its own cursor with monotonic dates", i, sha)
		}
		// And the bound must actually be doing work: a cursor near HEAD must
		// not drag the whole history along with it.
		if i == len(shas)-1 && len(bounded) > 1 {
			t.Errorf("bounding at the newest commit returned %d commits, want 1 — the bound is not narrowing anything",
				len(bounded))
		}
	}
}
