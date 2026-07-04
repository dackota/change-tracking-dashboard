package chartdiff_test

import (
	"context"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_RenderExceedsTimeout_ReturnsExceededLimits proves acceptance
// criterion 4: a render that blocks past Config.RenderTimeout surfaces as
// ExceededLimits rather than hanging the caller.
func TestDiff_RenderExceedsTimeout_ReturnsExceededLimits(t *testing.T) {
	t.Parallel()

	blockUntilDone := make(chan struct{})
	t.Cleanup(func() { close(blockUntilDone) }) // let the goroutine finish so the test process doesn't leak it

	repo := fixedParentRepo("parent-sha")
	renderer := &fakeRenderer{
		fn: func(int, string, map[string]interface{}) (*chartrender.Result, error) {
			<-blockUntilDone
			return &chartrender.Result{}, nil
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{RenderTimeout: 20 * time.Millisecond}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	start := time.Now()
	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})
	elapsed := time.Since(start)

	if outcome.Kind != chartdiff.ExceededLimits {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.ExceededLimits)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Diff took %v to return after a 20ms timeout, want it to return promptly", elapsed)
	}
}
