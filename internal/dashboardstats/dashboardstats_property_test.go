package dashboardstats_test

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/dashboardstats"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// repoPool is the small fixed set of repo names changesetBatch draws from,
// so the "distinct repos" invariant is exercised against real duplication
// within a generated batch rather than every repo happening to be unique.
var repoPool = []string{"repo-a", "repo-b", "repo-c"}

// changesetBatch is a quick.Generator producing adversarial batches of
// Changesets for the invariant property below: 0 to many Changesets drawn
// from the small repo pool (so repos repeat), each with 0 to a few Changes
// in a random Chart/value mix, and CommittedAt values that are frequently
// out of construction order and sometimes duplicated across Changesets.
type changesetBatch []changeset.Changeset

// Generate implements quick.Generator. See changesetBatch for the shape of
// what it produces, including the empty (n==0) case quick.Check's shrinking
// will also exercise.
func (changesetBatch) Generate(rnd *rand.Rand, size int) reflect.Value {
	const maxChangesets = 40
	const maxChangesPerSet = 5

	n := rnd.Intn(maxChangesets + 1)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	batch := make(changesetBatch, 0, n)
	for i := 0; i < n; i++ {
		numChanges := rnd.Intn(maxChangesPerSet + 1)
		changes := make([]changeset.Change, 0, numChanges)
		for j := 0; j < numChanges; j++ {
			kind := changeset.KindValue
			if rnd.Intn(2) == 0 {
				kind = changeset.KindChart
			}
			changes = append(changes, changeset.Change{
				Change: domain.Change{Repo: repoPool[rnd.Intn(len(repoPool))]},
				Kind:   kind,
			})
		}

		// A small, wrapping offset range makes duplicate and out-of-order
		// CommittedAt values common across the batch — both are
		// adversarial to a naive "last element wins" LastChangeAt
		// implementation.
		offsetHours := rnd.Intn(20) - 10
		batch = append(batch, changeset.Changeset{
			Repo:        repoPool[rnd.Intn(len(repoPool))],
			CommitSha:   fmt.Sprintf("sha-%d", i),
			Author:      "author",
			CommittedAt: base.Add(time.Duration(offsetHours) * time.Hour),
			Changes:     changes,
		})
	}
	return reflect.ValueOf(batch)
}

// deepCopyChangesets returns a deep copy of cs, so callers can snapshot
// input state before calling Compute and compare it afterward without the
// copy aliasing any slice the original references.
func deepCopyChangesets(cs []changeset.Changeset) []changeset.Changeset {
	out := make([]changeset.Changeset, len(cs))
	for i, c := range cs {
		changes := make([]changeset.Change, len(c.Changes))
		copy(changes, c.Changes)
		out[i] = c
		out[i].Changes = changes
	}
	return out
}

// changesetsEqual reports whether a and b carry the same observable values
// — used to assert Compute performs no mutation of its input.
func changesetsEqual(a, b []changeset.Changeset) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Repo != b[i].Repo || a[i].CommitSha != b[i].CommitSha ||
			a[i].Author != b[i].Author || !a[i].CommittedAt.Equal(b[i].CommittedAt) {
			return false
		}
		if len(a[i].Changes) != len(b[i].Changes) {
			return false
		}
		for j := range a[i].Changes {
			if a[i].Changes[j].Kind != b[i].Changes[j].Kind ||
				a[i].Changes[j].Repo != b[i].Changes[j].Repo {
				return false
			}
		}
	}
	return true
}

// metricsEqual compares two Metrics values field-by-field, using
// time.Time.Equal for LastChangeAt rather than struct equality.
func metricsEqual(a, b dashboardstats.Metrics) bool {
	return a.Changesets == b.Changesets &&
		a.Changes == b.Changes &&
		a.Repositories == b.Repositories &&
		a.ChartChanges == b.ChartChanges &&
		a.LastChangeAt.Equal(b.LastChangeAt)
}

// TestCompute_Invariants_Property asserts the structural invariants that
// must hold for every possible Changeset batch — not just the handful of
// examples tabulated in TestCompute:
//   - Changesets == len(input), Changes == sum of len(cs.Changes)
//   - 0 <= Repositories <= Changesets, and Repositories == count of distinct repos
//   - 0 <= ChartChanges <= Changes, and ChartChanges == count of KindChart Changes
//   - LastChangeAt == the max CommittedAt (zero iff the input is empty), and is
//     never in the future of any input Changeset's CommittedAt
//   - Compute never mutates its input
//   - Compute is order-independent: shuffling the input yields equal Metrics
func TestCompute_Invariants_Property(t *testing.T) {
	t.Parallel()

	property := func(batch changesetBatch) bool {
		input := []changeset.Changeset(batch)
		before := deepCopyChangesets(input)

		got := dashboardstats.Compute(input)

		// Re-derive the expected values independently of the
		// implementation under test.
		wantChanges := 0
		wantChart := 0
		distinctRepos := map[string]struct{}{}
		var wantLast time.Time
		for _, cs := range input {
			distinctRepos[cs.Repo] = struct{}{}
			wantChanges += len(cs.Changes)
			for _, c := range cs.Changes {
				if c.Kind == changeset.KindChart {
					wantChart++
				}
			}
			if cs.CommittedAt.After(wantLast) {
				wantLast = cs.CommittedAt
			}
		}

		if got.Changesets != len(input) ||
			got.Changes != wantChanges ||
			got.Repositories != len(distinctRepos) ||
			got.ChartChanges != wantChart ||
			!got.LastChangeAt.Equal(wantLast) {
			return false
		}

		if got.Repositories < 0 || got.Repositories > got.Changesets {
			return false
		}
		if got.ChartChanges < 0 || got.ChartChanges > got.Changes {
			return false
		}

		// LastChangeAt is never in the future of any input CommittedAt, and
		// is exactly zero when (and, for this generator, only when) the
		// input is empty.
		for _, cs := range input {
			if cs.CommittedAt.After(got.LastChangeAt) {
				return false
			}
		}
		if len(input) == 0 && !got.LastChangeAt.IsZero() {
			return false
		}

		// No mutation of the input.
		if !changesetsEqual(before, input) {
			return false
		}

		// Order independence: shuffling the input must not change the
		// result.
		shuffled := deepCopyChangesets(input)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if !metricsEqual(dashboardstats.Compute(shuffled), got) {
			return false
		}

		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Error(err)
	}
}
