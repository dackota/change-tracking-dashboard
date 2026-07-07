package chartdiff_test

import (
	"context"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
)

// TestDiff_ThreadsConfiguredMaterializeBoundsToChartRepo proves
// Engine.compute's gitsource.MaterializeBounds{...} construction actually
// carries every one of Config's materialization ceilings through to
// ChartRepo.MaterializeSubtreeBounded — including MaxMaterializedNodes (the
// new tree-node ceiling) alongside the pre-existing bytes/files/depth
// fields — rather than only proving the end effect (ExceededLimits) for one
// of them via a real repository (realrepo_test.go already does that for
// MaxMaterializedBytes).
func TestDiff_ThreadsConfiguredMaterializeBoundsToChartRepo(t *testing.T) {
	t.Parallel()

	var observed []gitsource.MaterializeBounds
	repo := &fakeChartRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(_, _, _ string, bounds gitsource.MaterializeBounds) error {
			observed = append(observed, bounds)
			return nil
		},
	}
	renderer := &fakeRenderer{}

	cfg := chartdiff.Config{
		MaxMaterializedBytes: 12345,
		MaxMaterializedFiles: 42,
		MaxMaterializedDepth: 7,
		MaxMaterializedNodes: 99,
	}
	engine, err := chartdiff.NewEngine(cfg, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

	if len(observed) != 2 {
		t.Fatalf("MaterializeSubtreeBounded called %d times, want 2 (old + new side)", len(observed))
	}
	for i, got := range observed {
		want := gitsource.MaterializeBounds{MaxTotalBytes: 12345, MaxFiles: 42, MaxDepth: 7, MaxTreeNodes: 99}
		if got != want {
			t.Errorf("call %d: bounds = %+v, want %+v", i, got, want)
		}
	}
}
