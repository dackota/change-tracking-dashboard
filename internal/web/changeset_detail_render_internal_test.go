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

// TestRepoShortName_ExactExamples pins the exact short name for the specific
// .git / trailing-slash permutations the double-slash regression was
// reported against (repoShortName("https://github.com/org/repo/.git")
// returning the whole URL instead of "repo") plus its close siblings, and
// the pre-existing local-path / no-suffix / empty-fallback behaviors that
// must not regress — mirrors TestCommitURL_ExactExamples above.
func TestRepoShortName_ExactExamples(t *testing.T) {
	cases := []struct {
		name string
		repo string
		want string
	}{
		{"local path", "/repos/free-tier-oracle-cloud-k8s", "free-tier-oracle-cloud-k8s"},
		{"plain URL, no .git", "https://github.com/o/r", "r"},
		{"trailing .git, no extra slash", "https://github.com/o/r.git", "r"},
		{"trailing slash then .git (the reported bug)", "https://github.com/org/repo/.git", "repo"},
		{"trailing .git then slash", "https://github.com/org/repo.git/", "repo"},
		{"trailing slash, .git, slash", "https://github.com/org/repo/.git/", "repo"},
		{"repeated slashes around .git", "https://github.com/org/repo//.git//", "repo"},
		{"degenerate reduction falls back to original", ".git", ".git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := repoShortName(tc.repo); got != tc.want {
				t.Errorf("repoShortName(%q) = %q, want %q", tc.repo, got, tc.want)
			}
		})
	}
}

// repoShortNameBases pairs each base repo path/URL with the short name it
// must always reduce to, regardless of which trailing-slash/".git" suffix
// (commitURLTrailingSuffixes, below) is appended to it.
var repoShortNameBases = []struct {
	base string
	want string
}{
	{"https://github.com/org/repo", "repo"},
	{"http://example.com/org/repo", "repo"},
	{"https://gitlab.example.com/group/sub/repo", "repo"},
	{"/repos/free-tier-oracle-cloud-k8s", "free-tier-oracle-cloud-k8s"},
}

// repoShortNameCase is a generated (repo, want) pair for the property test
// below: a base repo path/URL combined with one of the same trailing-slash /
// ".git"-suffix permutations commitURL's own property test sweeps
// (commitURLTrailingSuffixes) — repoShortName must reduce every one of them
// down to the same clean base name.
type repoShortNameCase struct {
	repo string
	want string
}

// Generate implements quick.Generator, picking one base and one trailing-
// suffix permutation per draw so repeated runs sweep the whole combinatorial
// class rather than a fixed hand-picked subset of it.
func (repoShortNameCase) Generate(rnd *rand.Rand, size int) reflect.Value {
	b := repoShortNameBases[rnd.Intn(len(repoShortNameBases))]
	suffix := commitURLTrailingSuffixes[rnd.Intn(len(commitURLTrailingSuffixes))]
	return reflect.ValueOf(repoShortNameCase{repo: b.base + suffix, want: b.want})
}

// TestRepoShortName_NoDanglingArtifactsInvariant_Property asserts, over the
// generated class of repo paths/URLs crossed with every .git / trailing-
// slash permutation, that repoShortName always reduces to the clean base
// name — no stray "/" (a path separator) or ".git" suffix left dangling.
// This subsumes the single reported example as one member of the class,
// catching sibling permutations a hand-picked table would miss.
func TestRepoShortName_NoDanglingArtifactsInvariant_Property(t *testing.T) {
	property := func(c repoShortNameCase) bool {
		got := repoShortName(c.repo)
		if got != c.want {
			t.Logf("repoShortName(%q) = %q, want %q", c.repo, got, c.want)
			return false
		}
		if strings.Contains(got, "/") {
			t.Logf("repoShortName(%q) = %q: contains a path separator", c.repo, got)
			return false
		}
		if strings.HasSuffix(got, ".git") {
			t.Logf("repoShortName(%q) = %q: still carries a .git suffix", c.repo, got)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Error(err)
	}
}

// TestRepoShortName_DegenerateInputsNeverPanicAndFallBackCleanly exercises
// inputs adjacent to the reported bug's class but degenerate enough that no
// clean base name exists — scheme-only, the literal ".git" path, a doubled
// ".git.git/" suffix (proving the fix strips exactly the trailing ".git"
// occurrence via the shared regex rather than looping or over-stripping),
// and bare repeated slashes. repoShortName must never panic on these, and
// per its documented contract must never fall back to an empty string for a
// non-empty input.
func TestRepoShortName_DegenerateInputsNeverPanicAndFallBackCleanly(t *testing.T) {
	cases := []struct {
		name string
		repo string
	}{
		{"empty string", ""},
		{"bare slash", "/"},
		{"repeated bare slashes", "///"},
		{"path is literally .git", ".git"},
		{"scheme-only, https", "https://"},
		{"scheme-only, http", "http://"},
		{"doubled .git suffix", "https://github.com/org/repo.git.git/"},
		{"doubled .git suffix with repeated slashes", "https://github.com/org/repo//.git.git//"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("repoShortName(%q) panicked: %v", tc.repo, r)
				}
			}()
			got := repoShortName(tc.repo)
			if got == "" && tc.repo != "" {
				t.Errorf("repoShortName(%q) = %q: fell back to empty instead of the original string", tc.repo, got)
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
