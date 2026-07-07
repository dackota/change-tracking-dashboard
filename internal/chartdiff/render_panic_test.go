package chartdiff_test

import (
	"context"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_RendererPanics_ReturnsCouldNotRenderInsteadOfCrashing proves the
// SHOULD-FIX render-goroutine recover: a Renderer.Render call that panics
// (a hostile or malformed chart could trigger a panic deep in the Helm SDK,
// outside this package's control) must not crash the whole dashboard
// process. Diff instead folds the panic into the safe, generic
// CouldNotRender classification, exactly like any other unclassified render
// failure — and the process (and this test) survives to prove it.
func TestDiff_RendererPanics_ReturnsCouldNotRenderInsteadOfCrashing(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	renderer := &fakeRenderer{
		fn: func(int, string, map[string]interface{}) (*chartrender.Result, error) {
			panic("boom: simulated Helm SDK panic on a hostile chart")
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

	if outcome.Kind != chartdiff.CouldNotRender {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.CouldNotRender)
	}
}
