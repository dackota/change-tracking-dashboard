// Package gitsource (this file): History, a walked file history and the two
// ways of narrowing one.
//
// Narrowing exists as a separate operation because a caller may need several
// different ranges out of a single walk. The poller is that caller: a tracker
// group shares one (Repo, FileGlob), so one walk per file serves every field
// in the group, but each field has its own cursor and its own backfill window.
// One walk, N ranges — the walk carries the union of what any field needs and
// each field takes its own slice.
//
// These live beside WalkCommits, not in the poller, so the "where does this
// range stop" rule has one owner. NotBefore and the walk's own time bound are
// the same rule applied at different moments, and they share the predicate
// below rather than agreeing by inspection.
package gitsource

import (
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// History is one file's walked commit history, oldest first.
type History []domain.CommitSnapshot

// Since returns the commits strictly after sha, together with the snapshot at
// sha itself.
//
// That snapshot is the point of the method: it is the "before" state a caller
// resuming from a cursor needs in order to diff the first new commit against
// something. A walk that merely stopped at sha would exclude it and leave the
// caller with no baseline.
//
// When sha is absent from the history — its commit was rewritten away, say —
// the whole history is returned with found=false, so a caller can tell
// "nothing new" from "no baseline available".
func (h History) Since(sha string) (rest History, at domain.CommitSnapshot, found bool) {
	for i, snap := range h {
		if snap.CommitSha == sha {
			return h[i+1:], snap, true
		}
	}
	return h, domain.CommitSnapshot{}, false
}

// NotBefore returns the commits from HEAD back to — but excluding — the first
// one older than bound. A zero bound narrows nothing.
//
// It stops at the boundary rather than filtering across it, which is the same
// thing WalkCommits does while walking (see outOfWindow): both consult one
// predicate, so a non-monotonic author date truncates identically whether the
// bound was applied during the walk or afterwards here.
func (h History) NotBefore(bound time.Time) History {
	if bound.IsZero() {
		return h
	}
	for i := len(h) - 1; i >= 0; i-- {
		if outOfWindow(h[i].CommittedAt, bound) {
			return h[i+1:]
		}
	}
	return h
}

// outOfWindow reports whether a commit at t falls outside a walk bounded below
// by bound. It is the single definition of that boundary, consulted by
// WalkCommits as it walks and by NotBefore as it slices — the two must agree,
// and sharing the predicate is how, rather than two comparisons that look
// alike.
//
// A zero bound is no bound: nothing is out of window.
func outOfWindow(t, bound time.Time) bool {
	return !bound.IsZero() && t.Before(bound)
}
