package chartdiff

import (
	"encoding/binary"
	"strings"
)

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
// RepoName and TenantPath (Request fields) are unvalidated, caller-supplied
// strings — nothing stops either from containing any byte value, including
// an embedded NUL — so a fixed-separator encoding (e.g. joining fields with
// "\x00") is not safe: two distinct 4-tuples whose fields differ only in
// where an embedded separator byte falls can render to byte-identical
// output, and singleflight would then hand the second caller the first
// caller's Outcome. String instead length-prefixes each field with its byte
// length (a fixed-width uint64, so the prefix itself can never be confused
// with field content), making the encoding provably injective: the length
// prefix immediately preceding each field is exactly that field's byte
// count, so the stream can always be split back into the same four fields
// it was built from, regardless of what bytes those fields contain. Two
// distinct field tuples can therefore never produce the same String()
// output.
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
