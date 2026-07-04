package chartdiff_test

import (
	"context"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
)

// TestDiff_RootCommit_ReturnsNoPriorVersion proves acceptance criterion 2's
// NoPriorVersion classification: a root commit (repo.FirstParent reports
// gitsource.ErrNoParent) never reaches the renderer at all.
func TestDiff_RootCommit_ReturnsNoPriorVersion(t *testing.T) {
	t.Parallel()

	repo := &fakeChartRepo{
		firstParentFn: func(string) (string, error) { return "", gitsource.ErrNoParent },
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "root-sha"})

	if outcome.Kind != chartdiff.NoPriorVersion {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.NoPriorVersion)
	}
	if renderer.callCount() != 0 {
		t.Errorf("renderer invoked %d times, want 0 (a root commit has nothing to render)", renderer.callCount())
	}
}

// TestDiff_DependencyNotVendored_ReturnsUnavailable proves acceptance
// criterion 2's Unavailable classification: chartrender reporting
// ReasonDependencyNotVendored on either side surfaces as Unavailable, with
// no internal detail (Missing dependency names, Helm error text) attached to
// the Outcome.
func TestDiff_DependencyNotVendored_ReturnsUnavailable(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	renderer := &fakeRenderer{
		fn: func(int, string, map[string]interface{}) (*chartrender.Result, error) {
			return nil, &chartrender.Failure{Reason: chartrender.ReasonDependencyNotVendored, Missing: []string{"subchart"}}
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

	if outcome.Kind != chartdiff.Unavailable {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.Unavailable)
	}
}

// TestDiff_MalformedChart_ReturnsCouldNotRender proves acceptance criterion
// 2's CouldNotRender classification for a malformed chart.
func TestDiff_MalformedChart_ReturnsCouldNotRender(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	renderer := &fakeRenderer{
		fn: func(int, string, map[string]interface{}) (*chartrender.Result, error) {
			return nil, &chartrender.Failure{Reason: chartrender.ReasonMalformedChart, Cause: context.DeadlineExceeded}
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

// TestDiff_UnclassifiedRenderError_ReturnsCouldNotRender proves that an
// error the renderer returns which isn't a *chartrender.Failure at all (a
// generic, unexpected error) still folds into the safe CouldNotRender
// bucket rather than propagating raw error text — Diff never leaks internal
// detail regardless of the failure's shape.
func TestDiff_UnclassifiedRenderError_ReturnsCouldNotRender(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	renderer := &fakeRenderer{
		fn: func(int, string, map[string]interface{}) (*chartrender.Result, error) {
			return nil, errUnexpected
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

// TestDiff_MaterializationExceedsBounds_ReturnsExceededLimits proves
// acceptance criterion 2's ExceededLimits classification for the
// materialization-side ceiling (gitsource.ErrMaterializeBoundsExceeded).
func TestDiff_MaterializationExceedsBounds_ReturnsExceededLimits(t *testing.T) {
	t.Parallel()

	repo := &fakeChartRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(string, string, string, gitsource.MaterializeBounds) error {
			return gitsource.ErrMaterializeBoundsExceeded
		},
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

	if outcome.Kind != chartdiff.ExceededLimits {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.ExceededLimits)
	}
	if renderer.callCount() != 0 {
		t.Errorf("renderer invoked %d times, want 0 (materialization failed before any render)", renderer.callCount())
	}
}

// TestDiff_UnclassifiedMaterializeError_ReturnsCouldNotRender proves that a
// materialize failure unrelated to bounds (e.g. the subtree not existing at
// that commit) folds into CouldNotRender, not ExceededLimits or a leaked
// error.
func TestDiff_UnclassifiedMaterializeError_ReturnsCouldNotRender(t *testing.T) {
	t.Parallel()

	repo := &fakeChartRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(string, string, string, gitsource.MaterializeBounds) error {
			return errUnexpected
		},
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

	if outcome.Kind != chartdiff.CouldNotRender {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.CouldNotRender)
	}
}

// TestDiff_UnclassifiedFirstParentError_ReturnsCouldNotRender proves that a
// FirstParent failure other than ErrNoParent (e.g. the commit doesn't exist)
// folds into CouldNotRender rather than propagating raw error text or
// panicking.
func TestDiff_UnclassifiedFirstParentError_ReturnsCouldNotRender(t *testing.T) {
	t.Parallel()

	repo := &fakeChartRepo{
		firstParentFn: func(string) (string, error) { return "", errUnexpected },
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

	if outcome.Kind != chartdiff.CouldNotRender {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.CouldNotRender)
	}
	if renderer.callCount() != 0 {
		t.Errorf("renderer invoked %d times, want 0", renderer.callCount())
	}
}
