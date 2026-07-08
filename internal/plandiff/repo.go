package plandiff

import "github.com/dackota/change-tracking-dashboard/internal/gitsource"

// PlanRepo is the git-access seam Engine.Diff depends on: resolving a
// commit's first parent, and materializing a Terraform stack/module subtree
// (bounded) into an on-disk directory a Parser can load. This is
// structurally identical to chartdiff.ChartRepo — *gitsource.Source
// satisfies both directly, with no adapter needed. Tests inject a fake to
// exercise classification, caching, timeout, and concurrency behavior
// without a real git repository.
type PlanRepo interface {
	// FirstParent resolves commitSha's first parent. It returns
	// gitsource.ErrNoParent (checked via errors.Is) for a root commit.
	FirstParent(commitSha string) (string, error)
	// MaterializeSubtreeBounded writes subtreePath as it existed at
	// commitSha into destDir, bounded by bounds. It returns
	// gitsource.ErrMaterializeBoundsExceeded (checked via errors.Is) if the
	// subtree exceeds bounds.
	MaterializeSubtreeBounded(commitSha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error
}
