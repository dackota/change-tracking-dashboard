// Package web (this file): internal (whitebox) tests for commitURL, the
// unexported commit-link builder in changeset_detail_render.go — kept
// behaviorally consistent with its client-side counterpart, timeline.js's
// commitURL (see internal/web/static/commit-link.property.test.js).
package web

import (
	"math/rand"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
)

// TestCommitURL_ExactExamples pins the exact rendered URL for the specific
// .git / trailing-slash permutations the double-slash regression was
// reported against ("https://github.com/org/repo/.git" rendering
// ".../repo//commit/<sha>") plus its close siblings, so the originally
// reported shape has a deterministic, always-run regression case regardless
// of what the randomized property test below happens to draw.
func TestCommitURL_ExactExamples(t *testing.T) {
	cases := []struct {
		name string
		repo string
		sha  string
		want string
	}{
		{"no suffix", "https://github.com/org/repo", "abc123", "https://github.com/org/repo/commit/abc123"},
		{"trailing slash", "https://github.com/org/repo/", "abc123", "https://github.com/org/repo/commit/abc123"},
		{"trailing .git", "https://github.com/org/repo.git", "abc123", "https://github.com/org/repo/commit/abc123"},
		{"trailing slash then .git (the reported bug)", "https://github.com/org/repo/.git", "abc123", "https://github.com/org/repo/commit/abc123"},
		{"trailing .git then slash", "https://github.com/org/repo.git/", "abc123", "https://github.com/org/repo/commit/abc123"},
		{"trailing slash, .git, slash", "https://github.com/org/repo/.git/", "abc123", "https://github.com/org/repo/commit/abc123"},
		{"repeated slashes around .git", "https://github.com/org/repo//.git//", "abc123", "https://github.com/org/repo/commit/abc123"},
		{"non-http repo yields no link", "git@github.com:org/repo.git", "abc123", ""},
		{"empty sha yields no link", "https://github.com/org/repo", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commitURL(tc.repo, tc.sha); got != tc.want {
				t.Errorf("commitURL(%q, %q) = %q, want %q", tc.repo, tc.sha, got, tc.want)
			}
		})
	}
}

// commitURLRepoCase is a generated (repo, sha) pair for the property test
// below: an http(s) base repo URL combined with one of the trailing-slash /
// ".git"-suffix permutations in the class this fix must handle.
type commitURLRepoCase struct {
	repo string
	sha  string
}

var commitURLBases = []string{
	"https://github.com/org/repo",
	"http://example.com/org/repo",
	"https://gitlab.example.com/group/sub/repo",
}

// commitURLTrailingSuffixes enumerates the trailing-slash / ".git"
// combinations in the class: no suffix, slash(es) alone, a bare ".git", and
// ".git" preceded and/or followed by one or more slashes.
var commitURLTrailingSuffixes = []string{
	"", "/", "//",
	".git", ".git/", ".git//",
	"/.git", "/.git/", "/.git//",
	"//.git", "//.git//",
}

// Generate implements quick.Generator, picking one base repo and one
// trailing-suffix permutation per draw so repeated runs sweep the whole
// combinatorial class rather than a fixed hand-picked subset of it.
func (commitURLRepoCase) Generate(rnd *rand.Rand, size int) reflect.Value {
	base := commitURLBases[rnd.Intn(len(commitURLBases))]
	suffix := commitURLTrailingSuffixes[rnd.Intn(len(commitURLTrailingSuffixes))]
	sha := "sha-" + strconv.Itoa(rnd.Int())
	return reflect.ValueOf(commitURLRepoCase{repo: base + suffix, sha: sha})
}

// commitURLDoubleSlashInPath matches a "//" that appears anywhere after the
// scheme's own "://" authority separator — i.e. an empty path segment, the
// exact shape of the reported bug ("https://.../repo//commit/<sha>").
var commitURLDoubleSlashInPath = regexp.MustCompile(`[a-z]+://.*//`)

// TestCommitURL_NoDoubleSlashInvariant_Property asserts, over the generated
// class of http(s) repo URLs crossed with every .git / trailing-slash
// permutation, that commitURL never produces a URL with an empty path
// segment and always ends with the exact "/commit/<sha>" suffix. This
// subsumes the single reported example (".../repo/.git") as one member of
// the class, catching sibling permutations a hand-picked example would miss.
func TestCommitURL_NoDoubleSlashInvariant_Property(t *testing.T) {
	property := func(c commitURLRepoCase) bool {
		got := commitURL(c.repo, c.sha)
		if got == "" {
			t.Logf("commitURL(%q, %q) returned empty for an http(s) repo + non-empty sha", c.repo, c.sha)
			return false
		}
		if strings.Count(got, "://") != 1 {
			t.Logf("commitURL(%q, %q) = %q: want exactly one \"://\"", c.repo, c.sha, got)
			return false
		}
		if commitURLDoubleSlashInPath.MatchString(got) {
			t.Logf("commitURL(%q, %q) = %q: double slash in path", c.repo, c.sha, got)
			return false
		}
		want := "/commit/" + c.sha
		if !strings.HasSuffix(got, want) {
			t.Logf("commitURL(%q, %q) = %q: want suffix %q", c.repo, c.sha, got, want)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Error(err)
	}
}
