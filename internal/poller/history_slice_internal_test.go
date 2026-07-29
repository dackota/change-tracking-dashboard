// Package poller (internal test): asserts the invariant the whole grouped
// walk rests on — that slicing ONE shared history with fileHistory.since /
// fileHistory.notBefore yields exactly the snapshot set a dedicated
// gitsource.WalkCommits call with that field's own bounds would have returned.
//
// Everything in PollGroup's equivalence argument reduces to this property: if
// slicing ever diverges from walking, some field silently loses or gains
// history. It is asserted here over every boundary the real code can hit
// (every commit's exact timestamp, both sides of it, the extremes, every
// present SHA and an absent one) rather than a handful of examples, and over
// a repo whose author dates are deliberately NON-monotonic with respect to
// committer-time walk order — the case where "break at the boundary" and
// "filter by the boundary" part ways.
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
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// buildSkewedDateRepo commits one file repeatedly with monotonically
// increasing COMMITTER times (which fix git log's walk order) but shuffled
// AUTHOR times (which is what the notBefore boundary is tested against). The
// two orders therefore disagree, which is exactly when break-at-boundary and
// filter-by-boundary can diverge.
func buildSkewedDateRepo(t *testing.T) (repoPath, relPath string) {
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

	const rel = "config.yaml"
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	// Author-time offsets in days, deliberately out of order relative to the
	// commit sequence (and thus to committer time).
	authorDays := []int{0, 9, 3, 12, 1, 7, 5, 20, 2, 15}

	for i, d := range authorDays {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(fmt.Sprintf("v: %d\n", i)), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("git add: %v", err)
		}
		if _, err := wt.Commit(fmt.Sprintf("c%d", i), &git.CommitOptions{
			Author: &object.Signature{Name: "dev", Email: "d@x.com",
				When: base.AddDate(0, 0, d)},
			Committer: &object.Signature{Name: "ci", Email: "c@x.com",
				When: base.AddDate(0, 0, 100+i)}, // strictly increasing
		}); err != nil {
			t.Fatalf("commit c%d: %v", i, err)
		}
	}

	return dir, rel
}

// shas renders a snapshot slice as its commit SHAs, for comparison.
func shas(snaps []domain.CommitSnapshot) []string {
	out := make([]string, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, s.CommitSha)
	}
	return out
}

// assertSameSHAs fails unless got and want are the same sequence of SHAs.
func assertSameSHAs(t *testing.T, label string, got, want []domain.CommitSnapshot) {
	t.Helper()

	g, w := shas(got), shas(want)
	if len(g) != len(w) {
		t.Errorf("%s: sliced %d commits, direct walk returned %d\n sliced: %v\n direct: %v",
			label, len(g), len(w), g, w)
		return
	}
	for i := range w {
		if g[i] != w[i] {
			t.Errorf("%s: commit %d = %s, direct walk returned %s", label, i, g[i], w[i])
			return
		}
	}
}

// TestFileHistory_NotBefore_MatchesDirectWalk asserts that re-applying a
// field's own backfill boundary to the group's shared history equals walking
// with that boundary in the first place — for every boundary the real code
// can produce, over a repo with non-monotonic author dates.
func TestFileHistory_NotBefore_MatchesDirectWalk(t *testing.T) {
	t.Parallel()

	repoPath, relPath := buildSkewedDateRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	ctx := context.Background()

	full, err := src.WalkCommits(ctx, relPath, "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits (full): %v", err)
	}
	if len(full) == 0 {
		t.Fatal("fixture produced no history")
	}
	history := fileHistory(full)

	// Every commit's exact author time, plus a nanosecond either side of it
	// (the inclusive/exclusive boundary), plus the unbounded and
	// entirely-out-of-range extremes.
	bounds := []time.Time{
		{}, // zero: unbounded
		full[0].CommittedAt.AddDate(-10, 0, 0),
		full[len(full)-1].CommittedAt.AddDate(10, 0, 0),
	}
	for _, snap := range full {
		bounds = append(bounds,
			snap.CommittedAt.Add(-time.Nanosecond),
			snap.CommittedAt,
			snap.CommittedAt.Add(time.Nanosecond))
	}

	for _, bound := range bounds {
		want, err := src.WalkCommits(ctx, relPath, "", bound)
		if err != nil {
			t.Fatalf("WalkCommits (notBefore=%v): %v", bound, err)
		}
		assertSameSHAs(t, fmt.Sprintf("notBefore(%v)", bound), history.notBefore(bound), want)
	}
}

// TestFileHistory_Since_MatchesDirectWalk asserts that slicing a field's own
// cursor out of the shared history equals walking with that cursor as the stop
// commit — for every SHA in the history, and for a SHA absent from it (the
// rewritten-cursor case, where WalkCommits walks to the root).
func TestFileHistory_Since_MatchesDirectWalk(t *testing.T) {
	t.Parallel()

	repoPath, relPath := buildSkewedDateRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	ctx := context.Background()

	full, err := src.WalkCommits(ctx, relPath, "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits (full): %v", err)
	}
	history := fileHistory(full)

	// A well-formed SHA that is not in this history at all.
	const absentSha = "0123456789abcdef0123456789abcdef01234567"

	for _, sha := range append(shas(full), absentSha) {
		want, err := src.WalkCommits(ctx, relPath, sha, time.Time{})
		if err != nil {
			t.Fatalf("WalkCommits (since=%s): %v", sha, err)
		}
		rest, at, found := history.since(sha)
		assertSameSHAs(t, "since("+sha+")", rest, want)

		wantFound := sha != absentSha
		if found != wantFound {
			t.Errorf("since(%s) found = %v, want %v", sha, found, wantFound)
		}
		if found && at.CommitSha != sha {
			t.Errorf("since(%s) baseline = %s, want the cursor's own snapshot", sha, at.CommitSha)
		}
	}
}
