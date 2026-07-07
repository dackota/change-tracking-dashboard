package chartdiff_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_CacheDeterminism_Property asserts the invariant that must hold
// for any number of concurrent callers requesting the same
// (repo, tenantPath, parentSha, commitSha) key: every caller receives an
// identical Outcome, and the renderer is invoked at most once per side (old,
// new) across the whole burst — single-flight dedup, not just a
// post-hoc cache hit. Run with -race, this also proves the cache and
// single-flight group are correctly synchronized under real concurrent
// access, not just correct in a single-goroutine trace.
func TestDiff_CacheDeterminism_Property(t *testing.T) {
	t.Parallel()

	const callers = 50

	repo := fixedParentRepo("parent-sha")
	renderer := &fakeRenderer{
		fn: func(callN int, _ string, _ map[string]interface{}) (*chartrender.Result, error) {
			time.Sleep(5 * time.Millisecond) // widen the race window so concurrent callers actually overlap
			return &chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: "app", YAML: "v: 1\n"}}}, nil
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{ConcurrencyCap: callers}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	req := chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"}

	outcomes := make([]chartdiff.Outcome, callers)
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
		if got != outcomes[0] {
			t.Errorf("outcomes[%d] = %+v, want identical to outcomes[0] = %+v", i, got, outcomes[0])
		}
	}

	// Both sides (old, new) are rendered exactly once across the whole
	// concurrent burst — single-flight dedup, not N independent computations
	// that happen to agree.
	if got := renderer.callCount(); got != 2 {
		t.Errorf("renderer invoked %d times across %d concurrent callers for the same key, want exactly 2 (old + new, once each)", got, callers)
	}
}
