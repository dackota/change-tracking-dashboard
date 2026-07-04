package chartdiff

// cacheKey identifies one Chart diff computation: the repo, the tenant chart
// directory, and both sides of the diff (the resolved first-parent SHA and
// the change commit SHA). Per ADR 0002, the cache stores the Outcome —
// including a classified failure — keyed on this tuple, so a known-bad,
// known-unavailable, or known-exceeded-limits render is never re-attempted.
//
// A root commit (no first parent) never has a parentSha to key on; Engine.Diff
// classifies that case as NoPriorVersion before a cacheKey is ever
// constructed, so NoPriorVersion outcomes are not cached (recomputing "does
// this commit have a parent" is a single cheap git lookup, not worth the
// cache slot).
type cacheKey struct {
	repoName   string
	tenantPath string
	parentSha  string
	commitSha  string
}

// String renders k as a single string for use as a singleflight.Group key.
// The NUL separator can't appear in any of the four fields' legitimate
// values (a repo name, a filesystem path, or a git SHA), so distinct field
// tuples can never collide onto the same string.
func (k cacheKey) String() string {
	return k.repoName + "\x00" + k.tenantPath + "\x00" + k.parentSha + "\x00" + k.commitSha
}
