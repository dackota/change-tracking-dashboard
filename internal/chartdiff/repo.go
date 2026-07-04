package chartdiff

import "github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"

// ChartRepo is the git-access seam Engine.Diff depends on: resolving a
// commit's first parent, and materializing a tenant chart subtree (bounded)
// into an on-disk directory chartrender can load. *gitsource.Source
// satisfies this interface directly — production callers pass one in
// unmodified. Tests inject a fake to exercise classification, caching,
// timeout, and concurrency behavior without a real git repository.
type ChartRepo interface {
	// FirstParent resolves commitSha's first parent. It returns
	// gitsource.ErrNoParent (checked via errors.Is) for a root commit.
	FirstParent(commitSha string) (string, error)
	// MaterializeSubtreeBounded writes subtreePath as it existed at
	// commitSha into destDir, bounded by bounds. It returns
	// gitsource.ErrMaterializeBoundsExceeded (checked via errors.Is) if the
	// subtree exceeds bounds.
	MaterializeSubtreeBounded(commitSha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error
}
