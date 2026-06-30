package gitsource_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// buildFixtureRepo creates a temporary git repo with two commits to a Chart.yaml.
// Returns the repo path and the SHAs of commit1 and commit2.
func buildFixtureRepo(t *testing.T) (repoPath, sha1, sha2 string) {
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

	chartPath := filepath.Join(dir, "Chart.yaml")

	// Commit 1 — version 1.0.0
	if err := os.WriteFile(chartPath, []byte("version: \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatalf("write chart v1: %v", err)
	}
	if _, err := wt.Add("Chart.yaml"); err != nil {
		t.Fatalf("git add (c1): %v", err)
	}
	c1, err := wt.Commit("chore: initial Chart.yaml", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "alice",
			Email: "alice@example.com",
			When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}
	sha1 = c1.String()

	// Commit 2 — version 1.1.0
	if err := os.WriteFile(chartPath, []byte("version: \"1.1.0\"\n"), 0o644); err != nil {
		t.Fatalf("write chart v2: %v", err)
	}
	if _, err := wt.Add("Chart.yaml"); err != nil {
		t.Fatalf("git add (c2): %v", err)
	}
	c2, err := wt.Commit("feat: bump version", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "bob",
			Email: "bob@example.com",
			When:  time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}
	sha2 = c2.String()

	return dir, sha1, sha2
}

// buildThreeCommitRepo creates a fixture repo with three commits at known dates.
// commit 1 = Jan 1 2024, commit 2 = Jan 10 2024, commit 3 = Jan 20 2024.
func buildThreeCommitRepo(t *testing.T) (repoPath, sha1, sha2, sha3 string) {
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

	chartPath := filepath.Join(dir, "Chart.yaml")

	commit := func(version, msg string, when time.Time) string {
		if err := os.WriteFile(chartPath, []byte("version: \""+version+"\"\n"), 0o644); err != nil {
			t.Fatalf("write chart: %v", err)
		}
		if _, err := wt.Add("Chart.yaml"); err != nil {
			t.Fatalf("git add: %v", err)
		}
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "dev", Email: "d@x.com", When: when},
		})
		if err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
		return h.String()
	}

	sha1 = commit("1.0.0", "init", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	sha2 = commit("1.1.0", "bump1", time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC))
	sha3 = commit("1.2.0", "bump2", time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC))

	return dir, sha1, sha2, sha3
}

// TestWalkCommits_NotBefore_ExcludesOldCommits verifies that commits whose
// author-time is strictly before notBefore are excluded from the walk.
func TestWalkCommits_NotBefore_ExcludesOldCommits(t *testing.T) {
	t.Parallel()

	repoPath, _, sha2, sha3 := buildThreeCommitRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	// notBefore = Jan 5 2024 — should include sha2 (Jan 10) and sha3 (Jan 20),
	// but exclude sha1 (Jan 1).
	notBefore := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)
	snapshots, err := src.WalkCommits("Chart.yaml", "", notBefore)
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}

	if len(snapshots) != 2 {
		t.Fatalf("WalkCommits returned %d snapshots, want 2 (sha2 and sha3)", len(snapshots))
	}
	if snapshots[0].CommitSha != sha2 {
		t.Errorf("snapshots[0].CommitSha = %q, want sha2=%q", snapshots[0].CommitSha, sha2)
	}
	if snapshots[1].CommitSha != sha3 {
		t.Errorf("snapshots[1].CommitSha = %q, want sha3=%q", snapshots[1].CommitSha, sha3)
	}
}

// TestWalkCommits_NotBefore_ZeroMeansUnbounded verifies that the zero time.Time
// (notBefore.IsZero() == true) returns all commits, preserving the prior behavior.
func TestWalkCommits_NotBefore_ZeroMeansUnbounded(t *testing.T) {
	t.Parallel()

	repoPath, sha1, sha2, sha3 := buildThreeCommitRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	snapshots, err := src.WalkCommits("Chart.yaml", "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}

	if len(snapshots) != 3 {
		t.Fatalf("WalkCommits (zero bound) returned %d snapshots, want 3", len(snapshots))
	}
	shas := []string{sha1, sha2, sha3}
	for i, snap := range snapshots {
		if snap.CommitSha != shas[i] {
			t.Errorf("snapshots[%d].CommitSha = %q, want %q", i, snap.CommitSha, shas[i])
		}
	}
}

func TestWalkCommits_AllCommits(t *testing.T) {
	t.Parallel()

	repoPath, sha1, sha2 := buildFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	snapshots, err := src.WalkCommits("Chart.yaml", "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}

	if len(snapshots) != 2 {
		t.Fatalf("WalkCommits returned %d snapshots, want 2", len(snapshots))
	}

	// Snapshots should be in commit order (oldest first).
	if snapshots[0].CommitSha != sha1 {
		t.Errorf("snapshots[0].CommitSha = %q, want %q", snapshots[0].CommitSha, sha1)
	}
	if snapshots[1].CommitSha != sha2 {
		t.Errorf("snapshots[1].CommitSha = %q, want %q", snapshots[1].CommitSha, sha2)
	}

	// Verify content round-trip.
	if string(snapshots[0].Content) != "version: \"1.0.0\"\n" {
		t.Errorf("snapshots[0].Content = %q, want version 1.0.0", snapshots[0].Content)
	}
	if string(snapshots[1].Content) != "version: \"1.1.0\"\n" {
		t.Errorf("snapshots[1].Content = %q, want version 1.1.0", snapshots[1].Content)
	}

	// Verify author.
	if snapshots[0].Author != "alice" {
		t.Errorf("snapshots[0].Author = %q, want alice", snapshots[0].Author)
	}
	if snapshots[1].Author != "bob" {
		t.Errorf("snapshots[1].Author = %q, want bob", snapshots[1].Author)
	}
}

func TestWalkCommits_SinceHighWaterMark(t *testing.T) {
	t.Parallel()

	repoPath, sha1, sha2 := buildFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	// Walk since sha1 — should only return sha2.
	snapshots, err := src.WalkCommits("Chart.yaml", sha1, time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits (since sha1): %v", err)
	}

	if len(snapshots) != 1 {
		t.Fatalf("WalkCommits (since sha1) returned %d snapshots, want 1", len(snapshots))
	}
	if snapshots[0].CommitSha != sha2 {
		t.Errorf("snapshots[0].CommitSha = %q, want %q", snapshots[0].CommitSha, sha2)
	}
}

func TestWalkCommits_FilePath(t *testing.T) {
	t.Parallel()

	repoPath, _, _ := buildFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	snapshots, err := src.WalkCommits("Chart.yaml", "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}
	for _, snap := range snapshots {
		if snap.FilePath != "Chart.yaml" {
			t.Errorf("FilePath = %q, want Chart.yaml", snap.FilePath)
		}
	}
}

// --- Authenticated remote clone tests ---

// TestOpenOrClone_LocalPath_StillWorks verifies the existing local-path Open
// path is unaffected when no auth is provided.
func TestOpenOrClone_LocalPath_StillWorks(t *testing.T) {
	t.Parallel()

	repoPath, sha1, sha2 := buildFixtureRepo(t)
	localDest := t.TempDir()

	// Open a local repo path via OpenOrClone with nil auth.
	src, err := gitsource.OpenOrClone(repoPath, localDest, nil)
	if err != nil {
		t.Fatalf("OpenOrClone (local): %v", err)
	}

	snapshots, err := src.WalkCommits("Chart.yaml", "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}
	if snapshots[0].CommitSha != sha1 || snapshots[1].CommitSha != sha2 {
		t.Errorf("unexpected SHAs: %v", []string{snapshots[0].CommitSha, snapshots[1].CommitSha})
	}
}

// TestOpenOrClone_RemoteHTTPS_SendsBasicAuth verifies that when a remote HTTPS
// URL is given with BasicAuth credentials, go-git sends the Authorization header
// to the server. We use an httptest.Server that records requests.
func TestOpenOrClone_RemoteHTTPS_SendsBasicAuth(t *testing.T) {
	t.Parallel()

	// Track whether we received an Authorization header.
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		// Return 401 to keep the test fast — we only care that the header was sent.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	localDest := t.TempDir()
	auth := &gogithttp.BasicAuth{
		Username: "x-access-token",
		Password: "ghs_test_token_wiring",
	}

	// OpenOrClone will fail (server returns 401) but auth must have been attempted.
	_, err := gitsource.OpenOrClone(srv.URL+"/repo.git", localDest, auth)
	if err == nil {
		t.Fatal("expected error from unauthenticated server, got nil")
	}

	// Verify the Authorization header was set with the expected credentials.
	if receivedAuth == "" {
		t.Fatal("no Authorization header sent to server — auth not wired")
	}
	expectedCredential := base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_test_token_wiring"))
	if !strings.Contains(receivedAuth, expectedCredential) {
		t.Errorf("Authorization header %q does not contain expected basic auth credential", receivedAuth)
	}
}

// TestOpenOrClone_IdempotentOpen verifies that calling OpenOrClone a second time
// on an already-cloned local destination opens the existing repo (no re-clone).
func TestOpenOrClone_IdempotentOpen(t *testing.T) {
	t.Parallel()

	repoPath, sha1, _ := buildFixtureRepo(t)
	localDest := t.TempDir()

	// First call: clones to localDest.
	_, err := gitsource.OpenOrClone(repoPath, localDest, nil)
	if err != nil {
		t.Fatalf("OpenOrClone (first): %v", err)
	}

	// Second call: must open the existing clone without error.
	src2, err := gitsource.OpenOrClone(repoPath, localDest, nil)
	if err != nil {
		t.Fatalf("OpenOrClone (second / idempotent): %v", err)
	}

	snapshots, err := src2.WalkCommits("Chart.yaml", "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits after idempotent open: %v", err)
	}
	if len(snapshots) < 1 || snapshots[0].CommitSha != sha1 {
		t.Errorf("unexpected snapshots after idempotent open")
	}
}

// --- Authenticated fetch tests ---

// addCommitToRepo adds a new commit to an existing repo's worktree and returns
// the new commit SHA. Used to simulate a push to the "remote" after an initial clone.
func addCommitToRepo(t *testing.T, repoPath, version string) string {
	t.Helper()

	r, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("PlainOpen for addCommit: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree for addCommit: %v", err)
	}

	chartPath := filepath.Join(repoPath, "Chart.yaml")
	if err := os.WriteFile(chartPath, []byte("version: \""+version+"\"\n"), 0o644); err != nil {
		t.Fatalf("write chart for addCommit: %v", err)
	}
	if _, err := wt.Add("Chart.yaml"); err != nil {
		t.Fatalf("git add for addCommit: %v", err)
	}
	h, err := wt.Commit("bump: "+version, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "ci",
			Email: "ci@example.com",
			When:  time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("commit for addCommit: %v", err)
	}
	return h.String()
}

// TestFetch_NewCommitIsVisible is the critical behavioral test proving that
// authenticated fetch makes commits pushed after the initial clone visible via
// WalkCommits. This test MUST fail against the current code (no Fetch call) and
// pass after the fix.
func TestFetch_NewCommitIsVisible(t *testing.T) {
	t.Parallel()

	// Arrange: build a fixture repo (the "remote") with two commits, then clone it.
	remoteRepo, sha1, sha2 := buildFixtureRepo(t)
	localClone := t.TempDir()

	src, err := gitsource.OpenOrClone(remoteRepo, localClone, nil)
	if err != nil {
		t.Fatalf("initial OpenOrClone: %v", err)
	}

	// Confirm the clone sees the two initial commits.
	snapshots, err := src.WalkCommits("Chart.yaml", "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits before fetch: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 commits before fetch, got %d", len(snapshots))
	}

	// Act: add a new commit to the "remote" (simulates a push by another user).
	sha3 := addCommitToRepo(t, remoteRepo, "2.0.0")

	// Fetch from the remote into the existing clone.
	if err := src.Fetch(remoteRepo, nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Assert: WalkCommits now returns all three commits including the new one.
	snapshots, err = src.WalkCommits("Chart.yaml", "", time.Time{})
	if err != nil {
		t.Fatalf("WalkCommits after fetch: %v", err)
	}
	if len(snapshots) != 3 {
		t.Fatalf("WalkCommits after fetch returned %d snapshots, want 3", len(snapshots))
	}
	if snapshots[2].CommitSha != sha3 {
		t.Errorf("snapshots[2].CommitSha = %q, want sha3=%q", snapshots[2].CommitSha, sha3)
	}
	// Verify older commits are still present and in order.
	if snapshots[0].CommitSha != sha1 {
		t.Errorf("snapshots[0].CommitSha = %q, want sha1=%q", snapshots[0].CommitSha, sha1)
	}
	if snapshots[1].CommitSha != sha2 {
		t.Errorf("snapshots[1].CommitSha = %q, want sha2=%q", snapshots[1].CommitSha, sha2)
	}
}

// TestFetch_AlreadyUpToDate verifies that Fetch returns nil (not an error) when
// the remote has no new commits — "already up to date" is a normal, non-error state.
func TestFetch_AlreadyUpToDate(t *testing.T) {
	t.Parallel()

	remoteRepo, _, _ := buildFixtureRepo(t)
	localClone := t.TempDir()

	src, err := gitsource.OpenOrClone(remoteRepo, localClone, nil)
	if err != nil {
		t.Fatalf("initial OpenOrClone: %v", err)
	}

	// Fetch immediately — nothing has changed on the remote, must not error.
	if err := src.Fetch(remoteRepo, nil); err != nil {
		t.Errorf("Fetch on already-up-to-date repo returned error: %v", err)
	}
}

// TestFetch_RemoteHTTPS_SendsBasicAuth verifies that when an HTTPS URL is given
// with BasicAuth credentials, go-git sends the Authorization header on the fetch
// request. This mirrors the existing TestOpenOrClone_RemoteHTTPS_SendsBasicAuth
// pattern.
func TestFetch_RemoteHTTPS_SendsBasicAuth(t *testing.T) {
	t.Parallel()

	// First clone locally so we have a Source with a remote configured.
	remoteRepo, _, _ := buildFixtureRepo(t)
	localClone := t.TempDir()

	src, err := gitsource.OpenOrClone(remoteRepo, localClone, nil)
	if err != nil {
		t.Fatalf("initial OpenOrClone: %v", err)
	}

	// Track whether we received an Authorization header on the fetch.
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		// Return 401 — we only care that the header was sent, not that the fetch succeeds.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	auth := &gogithttp.BasicAuth{
		Username: "x-access-token",
		Password: "ghs_fetch_token_wiring",
	}

	// Fetch against the httptest server — it will fail (401) but auth must be sent.
	_ = src.Fetch(srv.URL+"/repo.git", auth)

	if receivedAuth == "" {
		t.Fatal("no Authorization header sent to server on fetch — auth not wired")
	}
	expectedCredential := base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_fetch_token_wiring"))
	if !strings.Contains(receivedAuth, expectedCredential) {
		t.Errorf("Authorization header %q does not contain expected basic auth credential", receivedAuth)
	}
}

// TestFetch_LocalPath_NoFetch verifies that calling Fetch on a Source opened
// via Open (local path, no remote) returns nil without error — local-path sources
// are never fetched.
func TestFetch_LocalPath_NoFetch(t *testing.T) {
	t.Parallel()

	repoPath, _, _ := buildFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	// A local-path Source has no remote; Fetch with empty remoteURL must be a no-op.
	if err := src.Fetch("", nil); err != nil {
		t.Errorf("Fetch on local-path source returned error: %v", err)
	}
}

// --- MatchingFiles (glob fan-out) tests ---

// buildGlobFixtureRepo creates a fixture repo with multiple files under "a/"
// that match the glob "a/*/Chart.yaml", plus one file that does not match.
func buildGlobFixtureRepo(t *testing.T) (repoPath string) {
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

	files := map[string]string{
		"a/x/Chart.yaml": "version: \"1.0.0\"\n",
		"a/y/Chart.yaml": "version: \"2.0.0\"\n",
		"a/y/notes.txt":  "not a chart\n",
		"b/Chart.yaml":   "version: \"3.0.0\"\n", // does not match a/*/Chart.yaml
	}

	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %q: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %q: %v", rel, err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("git add %q: %v", rel, err)
		}
	}

	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "alice", Email: "a@x.com",
			When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return dir
}

// TestMatchingFiles_ReturnsOnlyGlobMatches verifies that MatchingFiles enumerates
// the HEAD tree and returns exactly the blob paths matching the glob pattern,
// excluding non-matching files (including files matching a deeper-than-the-pattern
// or shallower-than-the-pattern shape).
func TestMatchingFiles_ReturnsOnlyGlobMatches(t *testing.T) {
	t.Parallel()

	repoPath := buildGlobFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	matches, err := src.MatchingFiles("a/*/Chart.yaml")
	if err != nil {
		t.Fatalf("MatchingFiles: %v", err)
	}

	want := []string{"a/x/Chart.yaml", "a/y/Chart.yaml"}
	if len(matches) != len(want) {
		t.Fatalf("MatchingFiles returned %v, want %v", matches, want)
	}
	for i, w := range want {
		if matches[i] != w {
			t.Errorf("matches[%d] = %q, want %q (matches=%v)", i, matches[i], w, matches)
		}
	}
}

// TestMatchingFiles_NoMatches verifies that a glob matching nothing returns an
// empty (not nil-panicking, not erroring) slice.
func TestMatchingFiles_NoMatches(t *testing.T) {
	t.Parallel()

	repoPath := buildGlobFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	matches, err := src.MatchingFiles("nope/*/nothing.yaml")
	if err != nil {
		t.Fatalf("MatchingFiles: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("MatchingFiles (no match) = %v, want empty", matches)
	}
}
