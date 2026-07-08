package plandiff

import (
	"encoding/binary"
	"strings"
)

// cacheKey identifies one plan-diff computation: the repo, the Terraform
// stack/module directory, and both sides of the diff (the resolved
// first-parent SHA and the change commit SHA) -- acceptance criterion 7's
// "repo/path/parent-sha/commit-sha key". The cache stores the Outcome --
// including a classified failure -- keyed on this tuple, so a known-bad or
// known-exceeded-limits diff is never re-attempted.
//
// A root commit (no first parent) never has a parentSha to key on;
// Engine.Diff classifies that case as NoPriorVersion before a cacheKey is
// ever constructed, so NoPriorVersion outcomes are not cached (recomputing
// "does this commit have a parent" is a single cheap git lookup, not worth
// the cache slot) -- mirroring chartdiff.cacheKey exactly.
type cacheKey struct {
	repoName   string
	tenantPath string
	parentSha  string
	commitSha  string
}

// String renders k as a single string for use as a singleflight.Group key.
// RepoName and TenantPath (Request fields) are unvalidated, caller-supplied
// strings, so a fixed-separator encoding is not injective-safe; String
// instead length-prefixes each field with its byte length (a fixed-width
// uint64), making the encoding provably injective -- see
// chartdiff.cacheKey.String's identical rationale, mirrored here verbatim.
func (k cacheKey) String() string {
	var b strings.Builder
	for _, field := range [...]string{k.repoName, k.tenantPath, k.parentSha, k.commitSha} {
		var lenPrefix [8]byte
		binary.BigEndian.PutUint64(lenPrefix[:], uint64(len(field)))
		b.Write(lenPrefix[:])
		b.WriteString(field)
	}
	return b.String()
}
