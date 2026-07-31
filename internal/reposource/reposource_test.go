// reposource_test.go covers behavior that previously lived in package main
// with no test file at all: which repos get credentials attached, when a
// working copy is reused versus re-opened, and that a remote-backed copy is
// refreshed on every hand-out.
package reposource_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/reposource"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// recordingTokenProvider counts how often a token was minted, so a test can
// tell "no credentials were attached" from "credentials were attached but
// unused".
type recordingTokenProvider struct {
	token string
	calls int
}

func (p *recordingTokenProvider) Token() (string, error) {
	p.calls++
	return p.token, nil
}

// fixtureRepo creates a local git repo with one committed file and returns its
// path.
func fixtureRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("version: 1.0.0\n"), 0o644); err != nil {
		t.Fatalf("write Chart.yaml: %v", err)
	}
	if _, err := wt.Add("Chart.yaml"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "dev", Email: "d@x.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

// TestGet_LocalPath_OpensTheRepo verifies the basic case: a tracked repo named
// by a local path is opened, and has no remote to fetch from.
func TestGet_LocalPath_OpensTheRepo(t *testing.T) {
	t.Parallel()

	c := reposource.New()
	src, err := c.Get(fixtureRepo(t))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if src == nil {
		t.Fatal("Get returned a nil Source")
	}
	if got := src.RemoteURL(); got != "" {
		t.Errorf("RemoteURL() = %q, want empty for a local-path repo", got)
	}
}

// TestGet_SameRepoTwice_ReturnsTheSameWorkingCopy is the point of the cache:
// the second call must hand back the copy already on disk rather than opening
// or cloning another one. For a remote repo, getting this wrong means
// re-cloning on every poll cycle.
func TestGet_SameRepoTwice_ReturnsTheSameWorkingCopy(t *testing.T) {
	t.Parallel()

	repo := fixtureRepo(t)
	c := reposource.New()

	first, err := c.Get(repo)
	if err != nil {
		t.Fatalf("Get (first): %v", err)
	}
	second, err := c.Get(repo)
	if err != nil {
		t.Fatalf("Get (second): %v", err)
	}
	if first != second {
		t.Error("Get returned a different Source on the second call — the working copy is not being reused")
	}
}

// TestGet_DistinctRepos_GetDistinctWorkingCopies verifies the cache is keyed
// by repo, not shared: two tracked repos must never collapse onto one working
// copy, which would silently attribute one repo's commits to the other.
func TestGet_DistinctRepos_GetDistinctWorkingCopies(t *testing.T) {
	t.Parallel()

	c := reposource.New()

	a, err := c.Get(fixtureRepo(t))
	if err != nil {
		t.Fatalf("Get (a): %v", err)
	}
	b, err := c.Get(fixtureRepo(t))
	if err != nil {
		t.Fatalf("Get (b): %v", err)
	}
	if a == b {
		t.Error("two distinct repos resolved to the same Source")
	}
}

// TestGet_UnopenableRepo_IsNotCached verifies a failed open is not remembered
// as a success. Caching a failure would make one bad poll cycle permanent for
// the life of the process, even after the repo became reachable.
func TestGet_UnopenableRepo_IsNotCached(t *testing.T) {
	t.Parallel()

	c := reposource.New()
	missing := filepath.Join(t.TempDir(), "not-a-repo")

	if _, err := c.Get(missing); err == nil {
		t.Fatal("Get on a non-repo path = nil error, want a failure")
	}

	// Turn the path into a real repo, then ask again. A cached failure (or a
	// cached nil Source) would keep failing here.
	if _, err := git.PlainInit(missing, false); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if _, err := c.Get(missing); err != nil {
		t.Errorf("Get after the repo became openable: %v — the earlier failure was cached", err)
	}
}

// TestGet_LocalPath_NeverMintsAToken is the credential boundary at its most
// important: a local-path repo is not an https:// remote, so no token is ever
// requested for it — not even when token auth is configured.
func TestGet_LocalPath_NeverMintsAToken(t *testing.T) {
	t.Parallel()

	tokens := &recordingTokenProvider{token: "ghs_should_never_be_used"}
	c := reposource.New(reposource.WithTokenProvider(tokens))

	repo := fixtureRepo(t)
	if _, err := c.Get(repo); err != nil {
		t.Fatalf("Get (first): %v", err)
	}
	// A second Get takes the already-cached path, which also consults the
	// credential rule before fetching.
	if _, err := c.Get(repo); err != nil {
		t.Fatalf("Get (second): %v", err)
	}

	if tokens.calls != 0 {
		t.Errorf("token minted %d times for a local-path repo, want 0", tokens.calls)
	}
}

// TestGet_PlainHTTPRemote_NeverMintsAToken pins the non-HTTPS guard: an
// http:// remote must never receive credentials, because they would cross the
// network in the clear. Config validation rejects http:// repos at load time,
// but this rule does not depend on that one having run — it is the last line
// of defense, and it is asserted independently for that reason.
//
// The clone itself fails (nothing is serving that URL); the assertion is about
// what was attempted before it failed, not about success.
func TestGet_PlainHTTPRemote_NeverMintsAToken(t *testing.T) {
	t.Parallel()

	tokens := &recordingTokenProvider{token: "ghs_should_never_be_used"}
	c := reposource.New(
		reposource.WithTokenProvider(tokens),
		reposource.WithCloneRoot(t.TempDir()),
	)

	if _, err := c.Get("http://127.0.0.1:1/insecure.git"); err == nil {
		t.Fatal("Get on an unreachable http:// remote = nil error, want a failure")
	}
	if tokens.calls != 0 {
		t.Errorf("token minted %d times for an http:// remote, want 0 — credentials must never go over plaintext", tokens.calls)
	}
}

// TestGet_HTTPSRemote_MintsAToken is the positive half of the credential rule:
// an https:// remote does get a token when a provider is configured. Without
// this, the guard above could pass trivially by never minting tokens at all.
func TestGet_HTTPSRemote_MintsAToken(t *testing.T) {
	t.Parallel()

	tokens := &recordingTokenProvider{token: "ghs_test_token"}
	c := reposource.New(
		reposource.WithTokenProvider(tokens),
		reposource.WithCloneRoot(t.TempDir()),
	)

	// Unreachable on purpose: the clone fails, after the credential decision
	// has already been made.
	if _, err := c.Get("https://127.0.0.1:1/private.git"); err == nil {
		t.Fatal("Get on an unreachable https:// remote = nil error, want a failure")
	}
	if tokens.calls == 0 {
		t.Error("no token minted for an https:// remote — authenticated access is not wired")
	}
}

// TestGet_NoTokenProvider_HTTPSRemoteIsUnauthenticated verifies the cache is
// usable with no credentials at all: a public https:// remote is attempted
// without auth rather than failing because no provider was configured.
func TestGet_NoTokenProvider_HTTPSRemoteIsUnauthenticated(t *testing.T) {
	t.Parallel()

	c := reposource.New(reposource.WithCloneRoot(t.TempDir()))

	// The failure must come from the unreachable remote, not from a missing
	// credential provider.
	_, err := c.Get("https://127.0.0.1:1/public.git")
	if err == nil {
		t.Fatal("Get on an unreachable https:// remote = nil error, want a failure")
	}
}

// TestGet_RemoteBackedCopy_SeesCommitsPushedSinceTheLastCall is the behavior
// the fetch-on-every-Get exists for: a caller about to walk a repo's history
// must see commits that landed since it last asked, without a restart.
func TestGet_RemoteBackedCopy_SeesCommitsPushedSinceTheLastCall(t *testing.T) {
	t.Parallel()

	// A local path serves as the "remote" here: OpenOrClone clones from it,
	// and the clone then fetches from it, which is the same code path a real
	// https:// remote takes minus the credentials.
	upstream := fixtureRepo(t)
	cloneDir := t.TempDir()

	src, err := gitsource.OpenOrClone(upstream, filepath.Join(cloneDir, "clone"), nil)
	if err != nil {
		t.Fatalf("OpenOrClone: %v", err)
	}

	before, err := src.WalkCommits(t.Context(), "Chart.yaml", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits (before): %v", err)
	}

	commitTo(t, upstream, "version: 2.0.0\n", "bump")

	if err := src.Fetch(nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	after, err := src.WalkCommits(t.Context(), "Chart.yaml", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits (after): %v", err)
	}
	if len(after) <= len(before) {
		t.Errorf("commit count did not grow after fetch (%d then %d) — a pushed commit stayed invisible",
			len(before), len(after))
	}
}

// TestResolveRepo_ReturnsTheSameWorkingCopyAsGet verifies the subtree.Resolver
// adapter is a view onto the same cache, not a second one: an on-demand diff
// request must reuse the working copy the poller already has, rather than
// cloning its own.
func TestResolveRepo_ReturnsTheSameWorkingCopyAsGet(t *testing.T) {
	t.Parallel()

	repo := fixtureRepo(t)
	c := reposource.New()

	viaGet, err := c.Get(repo)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	viaResolve, err := c.ResolveRepo(repo)
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	if viaResolve != viaGet {
		t.Error("ResolveRepo returned a different working copy than Get — the diff handlers are not sharing the poller's clones")
	}
}

// TestResolveRepo_PropagatesFailure verifies a failed resolution surfaces as
// an error rather than a nil subtree.Repo the caller would dereference.
func TestResolveRepo_PropagatesFailure(t *testing.T) {
	t.Parallel()

	c := reposource.New()
	got, err := c.ResolveRepo(filepath.Join(t.TempDir(), "not-a-repo"))
	if err == nil {
		t.Fatal("ResolveRepo on a non-repo path = nil error, want a failure")
	}
	if got != nil {
		t.Error("ResolveRepo returned a non-nil Repo alongside an error")
	}
}

// commitTo writes content to Chart.yaml in repoPath and commits it.
func commitTo(t *testing.T, repoPath, content, msg string) {
	t.Helper()

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("git open: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "Chart.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write Chart.yaml: %v", err)
	}
	if _, err := wt.Add("Chart.yaml"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "dev", Email: "d@x.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}
