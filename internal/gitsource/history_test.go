// Package gitsource (this file): asserts that narrowing an already-walked
// History agrees with having walked with that bound in the first place.
//
// This is the property the poller's grouped walk rests on. One walk per file
// serves every field in a tracker group, and each field then narrows to its
// own range — so if narrowing ever diverged from walking, a field would
// silently gain or lose history relative to what its own walk would have
// returned.
//
// It is asserted over every boundary the real code can hit (every commit's
// exact timestamp, both sides of it, and the extremes) rather than a handful
// of examples, and over a repo whose author dates are deliberately
// NON-monotonic with respect to committer-time walk order — the case where
// "break at the boundary" and "filter by the boundary" part ways.
package gitsource_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// historyShas renders a History as its commit SHAs, for comparison.
func historyShas(h gitsource.History) []string {
	out := make([]string, 0, len(h))
	for _, s := range h {
		out = append(out, s.CommitSha)
	}
	return out
}

// assertSameSHAs fails unless got and want are the same sequence of SHAs.
func assertSameSHAs(t *testing.T, label string, got, want gitsource.History) {
	t.Helper()

	g, w := historyShas(got), historyShas(want)
	if len(g) != len(w) {
		t.Errorf("%s: narrowed to %d commits, direct walk returned %d\n narrowed: %v\n   direct: %v",
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

// TestNotBefore_MatchesDirectWalk is the core equivalence: narrowing a fully
// walked History to a bound equals walking with that bound in the first place,
// for every boundary the real code can produce.
func TestNotBefore_MatchesDirectWalk(t *testing.T) {
	t.Parallel()

	repoPath, relPath := buildSkewedDateRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	ctx := context.Background()

	full, err := src.WalkCommits(ctx, relPath, time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits (full): %v", err)
	}
	if len(full) == 0 {
		t.Fatal("fixture produced no history")
	}

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
		want, err := src.WalkCommits(ctx, relPath, bound)
		if err != nil {
			t.Fatalf("WalkCommits (notBefore=%v): %v", bound, err)
		}
		assertSameSHAs(t, fmt.Sprintf("NotBefore(%v)", bound), full.NotBefore(bound), want)
	}
}

// TestNotBefore_IsIdempotentAndOrderIndependent verifies narrowing composes
// the way the poller relies on: the shared walk is already bounded by the
// widest bound in the group, and each field then applies its own narrower one
// to that result. Applying a bound to an already-narrowed History must equal
// applying it to the full one.
func TestNotBefore_IsIdempotentAndOrderIndependent(t *testing.T) {
	t.Parallel()

	repoPath, relPath := buildSkewedDateRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	full, err := src.WalkCommits(context.Background(), relPath, time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}

	for _, wide := range full {
		for _, narrow := range full {
			// The group's shared walk uses the wider (older) bound; the field
			// then applies its own.
			if narrow.CommittedAt.Before(wide.CommittedAt) {
				continue
			}
			got := full.NotBefore(wide.CommittedAt).NotBefore(narrow.CommittedAt)
			want := full.NotBefore(narrow.CommittedAt)
			assertSameSHAs(t,
				fmt.Sprintf("NotBefore(%v) then NotBefore(%v)", wide.CommittedAt, narrow.CommittedAt),
				got, want)
		}
	}
}

// TestSince_ReturnsTheCursorSnapshotAsBaseline pins what Since does that a
// walk stopping at the same SHA cannot: it hands back the snapshot AT the
// cursor, which is the "before" state the first new commit is diffed against.
// Losing it would silently turn a modification into an addition.
func TestSince_ReturnsTheCursorSnapshotAsBaseline(t *testing.T) {
	t.Parallel()

	repoPath, relPath := buildSkewedDateRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	full, err := src.WalkCommits(context.Background(), relPath, time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}
	if len(full) < 3 {
		t.Fatalf("fixture produced %d commits, need at least 3", len(full))
	}

	for i, cursor := range full {
		rest, at, found := full.Since(cursor.CommitSha)
		if !found {
			t.Errorf("Since(%s) reported the cursor as absent, but it is history[%d]", cursor.CommitSha, i)
			continue
		}
		if at.CommitSha != cursor.CommitSha {
			t.Errorf("Since(%s) baseline = %s, want the cursor commit itself", cursor.CommitSha, at.CommitSha)
		}
		assertSameSHAs(t, fmt.Sprintf("Since(history[%d])", i), rest, full[i+1:])
	}
}

// TestSince_AbsentCursorReportsNoBaseline covers the rewritten-history case: a
// cursor whose commit is no longer reachable yields the whole history and
// found=false, so a caller can tell "nothing new" from "no baseline to diff
// against".
func TestSince_AbsentCursorReportsNoBaseline(t *testing.T) {
	t.Parallel()

	repoPath, relPath := buildSkewedDateRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	full, err := src.WalkCommits(context.Background(), relPath, time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}

	rest, at, found := full.Since("0000000000000000000000000000000000000000")
	if found {
		t.Error("Since(absent sha) reported found=true")
	}
	if at.CommitSha != "" || at.Content != nil {
		t.Errorf("Since(absent sha) returned a baseline snapshot %+v, want the zero value", at)
	}
	assertSameSHAs(t, "Since(absent sha)", rest, full)
}

// TestSince_EmptyHistory verifies the degenerate case is not a panic: a cursor
// looked up in an empty history reports no baseline and nothing new.
func TestSince_EmptyHistory(t *testing.T) {
	t.Parallel()

	rest, at, found := gitsource.History{}.Since("abc123")
	if found {
		t.Error("Since on an empty history reported found=true")
	}
	if len(rest) != 0 {
		t.Errorf("Since on an empty history returned %d commits, want 0", len(rest))
	}
	if at.CommitSha != "" || at.Content != nil {
		t.Errorf("Since on an empty history returned a baseline %+v, want the zero value", at)
	}
}
