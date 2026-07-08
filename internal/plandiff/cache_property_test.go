package plandiff_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// TestDiff_CacheDeterminism_Property asserts the invariant that must hold
// for any number of concurrent callers requesting the same
// (repo, tenantPath, parentSha, commitSha) key (acceptance criterion 7):
// every caller receives an identical Outcome, and the parser is invoked at
// most once per side (old, new) across the whole burst -- single-flight
// dedup/coalescing, not just a post-hoc cache hit. Run with -race, this also
// proves the cache and single-flight group are correctly synchronized under
// real concurrent access.
func TestDiff_CacheDeterminism_Property(t *testing.T) {
	t.Parallel()

	const callers = 50

	repo := fixedParentRepo("parent-sha")
	parser := &fakeParser{
		fn: func(_ int, _ string) ([]plandiff.Resource, error) {
			time.Sleep(5 * time.Millisecond) // widen the race window so concurrent callers actually overlap
			return []plandiff.Resource{{Type: "t", Name: "n", Body: "v = 1\n"}}, nil
		},
	}

	engine, err := plandiff.NewEngine(plandiff.Config{ConcurrencyCap: callers}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	req := plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"}

	outcomes := make([]plandiff.Outcome, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		i := i
		go func() {
			defer wg.Done()
			outcomes[i] = engine.Diff(context.Background(), repo, req)
		}()
	}
	wg.Wait()

	for i, got := range outcomes {
		if got.Kind != outcomes[0].Kind || got.Diff.Unified != outcomes[0].Diff.Unified || got.Summary != outcomes[0].Summary {
			t.Errorf("outcomes[%d] = %+v, want identical to outcomes[0] = %+v", i, got, outcomes[0])
		}
	}

	// Both sides (old, new) are parsed exactly once across the whole
	// concurrent burst -- single-flight dedup, not N independent
	// computations that happen to agree.
	if got := parser.callCount(); got != 2 {
		t.Errorf("parser invoked %d times across %d concurrent callers for the same key, want exactly 2 (old + new, once each)", got, callers)
	}
}

// TestDiff_CacheDeterminism_FailureOutcomeAlsoCoalesces proves acceptance
// criterion 7's "including failures" clause: a burst of concurrent callers
// hitting a classified failure (not just the OK path) still coalesces to a
// single materialize/parse attempt per side.
func TestDiff_CacheDeterminism_FailureOutcomeAlsoCoalesces(t *testing.T) {
	t.Parallel()

	const callers = 30

	repo := fixedParentRepo("parent-sha")
	parser := &fakeParser{
		fn: func(int, string) ([]plandiff.Resource, error) {
			time.Sleep(5 * time.Millisecond)
			return nil, errUnexpected
		},
	}

	engine, err := plandiff.NewEngine(plandiff.Config{ConcurrencyCap: callers}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	req := plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"}

	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			outcome := engine.Diff(context.Background(), repo, req)
			if outcome.Kind != plandiff.CouldNotRender {
				t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.CouldNotRender)
			}
		}()
	}
	wg.Wait()

	if got := parser.callCount(); got != 1 {
		t.Errorf("parser invoked %d times across %d concurrent callers for the same failing key, want exactly 1 (old side fails before new is ever attempted)", got, callers)
	}
}
