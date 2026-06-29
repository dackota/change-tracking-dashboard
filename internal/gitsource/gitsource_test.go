package gitsource_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
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
