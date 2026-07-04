package chartdiff

import (
	"testing"
	"testing/quick"
)

// TestCacheKeyString_KnownColliding_NoLongerCollide is the concrete
// regression for the CRITICAL cacheKey ambiguous-delimiter collision: two
// distinct 4-tuples that only differ in where an embedded NUL byte falls
// used to render to byte-identical String() output under the old
// "\x00"-joined encoding. singleflight.Group.Do keys on this string, so a
// collision meant the second caller silently received the first caller's
// Outcome instead of its own.
func TestCacheKeyString_KnownColliding_NoLongerCollide(t *testing.T) {
	t.Parallel()

	a := cacheKey{repoName: "r", tenantPath: "a\x00b", parentSha: "p", commitSha: "c"}
	b := cacheKey{repoName: "r\x00a", tenantPath: "b", parentSha: "p", commitSha: "c"}

	if a == b {
		t.Fatalf("test setup invalid: a and b must be distinct tuples, got a == b == %+v", a)
	}
	if a.String() == b.String() {
		t.Errorf("a.String() == b.String() == %q for distinct cacheKeys a=%+v b=%+v, want distinct strings", a.String(), a, b)
	}
}

// TestCacheKeyString_InjectiveEncoding_Property asserts the invariant the
// CRITICAL fix must hold for every possible pair of cacheKeys, not just the
// one collision above: a.String() == b.String() if and only if all four
// fields are equal. This is what makes cacheKey.String() safe to use as a
// singleflight.Group key — two distinct computations can never be coalesced
// onto the same in-flight group, and two identical computations always are.
// The generated fields include adversarial values (embedded NUL bytes, empty
// strings) precisely because those are what the ad-hoc "\x00"-joined
// encoding got wrong.
//
// cacheKey's fields are unexported, and testing/quick.Value can only
// populate exported struct fields, so the property takes the eight raw
// strings directly (rather than two cacheKey values) and builds both keys
// from them inside the property function.
func TestCacheKeyString_InjectiveEncoding_Property(t *testing.T) {
	t.Parallel()

	property := func(aRepo, aTenant, aParent, aCommit, bRepo, bTenant, bParent, bCommit string) bool {
		a := cacheKey{repoName: aRepo, tenantPath: aTenant, parentSha: aParent, commitSha: aCommit}
		b := cacheKey{repoName: bRepo, tenantPath: bTenant, parentSha: bParent, commitSha: bCommit}

		same := a == b
		sameString := a.String() == b.String()
		return same == sameString
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 5000}); err != nil {
		t.Error(err)
	}
}
