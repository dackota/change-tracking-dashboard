package plandiff_test

import (
	"context"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// TestDiff_RootCommit_ReturnsNoPriorVersion proves acceptance criterion 6: a
// root commit (repo.FirstParent reports gitsource.ErrNoParent) never reaches
// the parser at all, and returns NoPriorVersion, not an error.
func TestDiff_RootCommit_ReturnsNoPriorVersion(t *testing.T) {
	t.Parallel()

	repo := &fakePlanRepo{
		firstParentFn: func(string) (string, error) { return "", gitsource.ErrNoParent },
	}
	parser := &fakeParser{}

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "root-sha"})

	if outcome.Kind != plandiff.NoPriorVersion {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.NoPriorVersion)
	}
	if parser.callCount() != 0 {
		t.Errorf("parser invoked %d times, want 0 (a root commit has nothing to parse)", parser.callCount())
	}
}

// TestDiff_MalformedHCL_ReturnsCouldNotRender proves acceptance criterion 5's
// "internal error detail is never returned" for a malformed/unparseable HCL
// failure: an unclassified parse error folds into the safe CouldNotRender
// bucket.
func TestDiff_MalformedHCL_ReturnsCouldNotRender(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	parser := &fakeParser{
		fn: func(int, string) ([]plandiff.Resource, error) {
			return nil, errUnexpected
		},
	}

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	if outcome.Kind != plandiff.CouldNotRender {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.CouldNotRender)
	}
}

// TestDiff_MaterializationExceedsBounds_ReturnsExceededLimits proves
// acceptance criterion 5's ExceededLimits classification for the
// materialization-side ceiling (gitsource.ErrMaterializeBoundsExceeded).
func TestDiff_MaterializationExceedsBounds_ReturnsExceededLimits(t *testing.T) {
	t.Parallel()

	repo := &fakePlanRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(string, string, string, gitsource.MaterializeBounds) error {
			return gitsource.ErrMaterializeBoundsExceeded
		},
	}
	parser := &fakeParser{}

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	if outcome.Kind != plandiff.ExceededLimits {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.ExceededLimits)
	}
	if parser.callCount() != 0 {
		t.Errorf("parser invoked %d times, want 0 (materialization failed before any parse)", parser.callCount())
	}
}

// TestDiff_UnclassifiedFirstParentError_ReturnsCouldNotRender proves that a
// FirstParent failure other than ErrNoParent folds into CouldNotRender
// rather than propagating raw error text or panicking.
func TestDiff_UnclassifiedFirstParentError_ReturnsCouldNotRender(t *testing.T) {
	t.Parallel()

	repo := &fakePlanRepo{
		firstParentFn: func(string) (string, error) { return "", errUnexpected },
	}
	parser := &fakeParser{}

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	if outcome.Kind != plandiff.CouldNotRender {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.CouldNotRender)
	}
	if parser.callCount() != 0 {
		t.Errorf("parser invoked %d times, want 0", parser.callCount())
	}
}
