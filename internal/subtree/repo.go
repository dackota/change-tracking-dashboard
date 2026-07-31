// Package subtree holds the git-access seam the on-demand diff engines
// depend on: resolving a commit's first parent, and materializing a bounded
// subtree at a commit into an on-disk directory those engines can read.
//
// The interface lives here, in one place, rather than being redeclared by
// each engine. Both chartdiff and plandiff need exactly this contract and
// nothing more, and the web slice needs to resolve a repo name to one — four
// declarations of a single two-method contract were four places for it to
// drift. *gitsource.Source satisfies Repo directly, with no adapter.
package subtree

import "github.com/dackota/change-tracking-dashboard/internal/gitsource"

// Repo is the git-access seam an on-demand diff engine depends on.
// *gitsource.Source satisfies this interface directly — production callers
// pass one in unmodified. Tests inject a fake to exercise classification,
// caching, timeout, and concurrency behavior without a real git repository.
type Repo interface {
	// FirstParent resolves commitSha's first parent. It returns
	// gitsource.ErrNoParent (checked via errors.Is) for a root commit.
	FirstParent(commitSha string) (string, error)
	// MaterializeSubtreeBounded writes subtreePath as it existed at
	// commitSha into destDir, bounded by bounds. It returns
	// gitsource.ErrMaterializeBoundsExceeded (checked via errors.Is) if the
	// subtree exceeds bounds.
	MaterializeSubtreeBounded(commitSha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error
}

// Resolver resolves a repo name (as carried on a Change/Changeset) to a Repo
// for a single diff computation. cmd/dashboard's sourceCache satisfies this
// over its existing get method.
//
// Callers must treat the repo name as untrusted until it has been checked
// against an already-ingested Changeset: a Resolver clones/fetches git URLs
// and opens local paths, so resolving an attacker-supplied name is itself
// the dangerous act. See web.ChartDiffHandler/PlanDiffHandler, which gate
// every call on ChangesetExistenceChecker first.
type Resolver interface {
	ResolveRepo(repo string) (Repo, error)
}
