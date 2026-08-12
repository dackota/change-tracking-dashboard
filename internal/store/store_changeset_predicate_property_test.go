package store_test

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

// impactTiers is the closed vocabulary the property test draws predicates
// from.
var impactTiers = []changeset.Impact{
	changeset.ImpactMajor,
	changeset.ImpactMinor,
	changeset.ImpactPatch,
	changeset.ImpactDowngrade,
	changeset.ImpactOther,
}

// bumpFor returns an (old, new) version pair whose classified impact is tier,
// so the property test can seed a corpus with a known tier mix.
func bumpFor(tier changeset.Impact) (string, string) {
	switch tier {
	case changeset.ImpactMajor:
		return "1.2.3", "2.0.0"
	case changeset.ImpactMinor:
		return "1.2.3", "1.3.0"
	case changeset.ImpactPatch:
		return "1.2.3", "1.2.4"
	case changeset.ImpactDowngrade:
		return "2.0.0", "1.9.0"
	default:
		return "not-a-version", "still-not-a-version"
	}
}

// naiveFilteredWalk is the oracle: fetch every changeset in the window with no
// predicate, then filter in Go. It is deliberately the dumbest correct
// implementation — no page-fill loop, no budget, no seek arithmetic — so that
// when it and QueryChangesets disagree, the page-fill loop is the suspect.
func naiveFilteredWalk(t *testing.T, s *store.Store, w store.TimeWindow, pred store.ChangesetPredicate) []string {
	t.Helper()

	var out []string
	cursor := ""
	for {
		page, err := s.QueryChangesets(w, filter.FilterSpec{}, nil, cursor, store.MaxChangesetPageSize)
		if err != nil {
			t.Fatalf("oracle QueryChangesets: %v", err)
		}
		for _, cs := range page.Changesets {
			if pred(cs) {
				out = append(out, cs.CommitSha)
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			return out
		}
	}
}

// filteredWalk walks the full result set through QueryChangesets with pred
// applied, following NextCursor until it is empty.
func filteredWalk(t *testing.T, s *store.Store, w store.TimeWindow, pred store.ChangesetPredicate, pageSize int) []string {
	t.Helper()

	var out []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5000 {
			t.Fatalf("filtered walk did not terminate; collected %d", len(out))
		}
		page := mustPage(t, s, w, filter.FilterSpec{}, pred, cursor, pageSize)
		out = append(out, shas(page.Changesets)...)
		cursor = page.NextCursor
		if cursor == "" {
			return out
		}
	}
}

// TestQueryChangesets_FilteredWalkMatchesNaiveOracle is the property test the
// whole page-fill design rests on: for any window, any predicate, and any page
// size, a full cursor walk through QueryChangesets yields exactly what
// fetching everything and filtering in Go yields — the same changesets, in the
// same order, each exactly once.
//
// This is the strongest statement of correctness available here, because it
// makes no claim about how the loop works. The loop can be rewritten
// completely and this test still holds it to the only thing that matters: the
// filtered walk and the naive filter are indistinguishable from outside.
//
// Randomization is seeded from a fixed constant so a failure is reproducible
// and CI is not flaky; the case parameters are logged with each subtest.
func TestQueryChangesets_FilteredWalkMatchesNaiveOracle(t *testing.T) {
	t.Parallel()

	const commits = 250

	s := newTestStore(t)

	rng := rand.New(rand.NewSource(0x5EED))

	// Seed a corpus with a random tier mix, plus multi-Change commits so
	// assembly boundaries are exercised alongside filtering.
	changes := make([]domain.Change, 0, commits*2)
	for i := range commits {
		tier := impactTiers[rng.Intn(len(impactTiers))]
		old, new := bumpFor(tier)
		sha := fmt.Sprintf("sha-%05d", i)
		c := versionCommit(sha, old, new, i)
		changes = append(changes, c)
		if rng.Intn(3) == 0 {
			// A second Change on the same commit, at a tier that does not
			// dominate the rollup, so the commit's tier stays interesting.
			extra := versionCommit(sha, "1.2.3", "1.2.4", i)
			extra.Field = "sidecar-version"
			changes = append(changes, extra)
		}
	}
	seedChanges(t, s, changes)

	full := store.TimeWindow{AsOf: predBase.Add(time.Duration(commits+1) * time.Minute)}

	for caseNum := range 40 {
		// A random non-empty allow-set over the closed tier vocabulary.
		allowed := make(map[changeset.Impact]struct{})
		for _, tier := range impactTiers {
			if rng.Intn(2) == 0 {
				allowed[tier] = struct{}{}
			}
		}
		if len(allowed) == 0 {
			allowed[impactTiers[rng.Intn(len(impactTiers))]] = struct{}{}
		}
		pred := func(cs changeset.Changeset) bool {
			_, ok := allowed[changeset.ClassifyImpact(cs)]
			return ok
		}

		// A random sub-window of the corpus, sometimes unbounded below.
		lo := rng.Intn(commits)
		hi := lo + 1 + rng.Intn(commits-lo)
		w := store.TimeWindow{
			Since: predBase.Add(time.Duration(lo) * time.Minute),
			AsOf:  predBase.Add(time.Duration(hi) * time.Minute),
		}
		if rng.Intn(4) == 0 {
			w = full
		}

		pageSize := 1 + rng.Intn(12)

		t.Run(fmt.Sprintf("case-%02d", caseNum), func(t *testing.T) {
			want := naiveFilteredWalk(t, s, w, pred)
			got := filteredWalk(t, s, w, pred, pageSize)

			if !equalStrings(got, want) {
				t.Errorf("filtered walk disagrees with the naive oracle\n"+
					"  window:   [%s, %s)\n"+
					"  pageSize: %d\n"+
					"  got  (%d): %v\n"+
					"  want (%d): %v",
					w.Since.Format(time.RFC3339), w.AsOf.Format(time.RFC3339),
					pageSize, len(got), got, len(want), want)
			}
		})
	}
}
