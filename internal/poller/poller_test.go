package poller_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/poller"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// buildFixtureRepo mirrors gitsource_test's helper; both need their own repo.
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

	chartPath := filepath.Join(dir, "apps", "tenant-zero", "dev", "us-west-2", "Chart.yaml")
	if err := os.MkdirAll(filepath.Dir(chartPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const relPath = "apps/tenant-zero/dev/us-west-2/Chart.yaml"

	write := func(version string) {
		if err := os.WriteFile(chartPath, []byte("version: \""+version+"\"\n"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	write("1.0.0")
	if _, err := wt.Add(relPath); err != nil {
		t.Fatalf("git add (c1): %v", err)
	}
	c1, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "alice", Email: "a@x.com",
			When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}
	sha1 = c1.String()

	write("1.1.0")
	if _, err := wt.Add(relPath); err != nil {
		t.Fatalf("git add (c2): %v", err)
	}
	c2, err := wt.Commit("bump", &git.CommitOptions{
		Author: &object.Signature{Name: "bob", Email: "b@x.com",
			When: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}
	sha2 = c2.String()

	return dir, sha1, sha2
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "poller_test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPoller_EndToEnd(t *testing.T) {
	t.Parallel()

	repoPath, _, _ := buildFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:         "aidp-version",
		ExtractorExpr: ".version",
		FacetPattern:  `^apps/(?P<tenant>[^/]+)/(?P<env>[^/]+)/(?P<region>[^/]+)/`,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	// Should have produced one Change (1.0.0 → 1.1.0).
	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("QueryFeed after poll: got %d changes, want 1", len(feed))
	}

	c := feed[0]
	if c.ChangeType != domain.ChangeTypeModified {
		t.Errorf("ChangeType = %q, want modified", c.ChangeType)
	}
	if c.OldValue == nil || *c.OldValue != "1.0.0" {
		t.Errorf("OldValue = %v, want 1.0.0", c.OldValue)
	}
	if c.NewValue == nil || *c.NewValue != "1.1.0" {
		t.Errorf("NewValue = %v, want 1.1.0", c.NewValue)
	}
	if c.Field != "aidp-version" {
		t.Errorf("Field = %q, want aidp-version", c.Field)
	}
	if c.Facets["tenant"] != "tenant-zero" {
		t.Errorf("facet tenant = %q, want tenant-zero", c.Facets["tenant"])
	}
	if c.Facets["env"] != "dev" {
		t.Errorf("facet env = %q, want dev", c.Facets["env"])
	}
}

func TestPoller_IncrementalPoll(t *testing.T) {
	t.Parallel()

	repoPath, _, _ := buildFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:         "aidp-version",
		ExtractorExpr: ".version",
		FacetPattern:  "",
	}

	p := poller.New(src, st)

	// First poll.
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll (first): %v", err)
	}

	feedAfterFirst, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed (first): %v", err)
	}
	firstCount := len(feedAfterFirst)

	// Second poll — high-water mark should prevent re-processing.
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll (second): %v", err)
	}

	feedAfterSecond, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed (second): %v", err)
	}

	if len(feedAfterSecond) != firstCount {
		t.Errorf("Second poll added %d unexpected changes (had %d, now %d)",
			len(feedAfterSecond)-firstCount, firstCount, len(feedAfterSecond))
	}
}

// buildSingleCommitRepo creates a fixture repo with exactly ONE commit.
func buildSingleCommitRepo(t *testing.T) (repoPath string) {
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
	if err := os.WriteFile(chartPath, []byte("version: \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := wt.Add("Chart.yaml"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "alice", Email: "a@x.com",
			When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return dir
}

func TestPoller_SingleCommitProducesAdded(t *testing.T) {
	t.Parallel()

	repoPath := buildSingleCommitRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  "",
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	// Single commit → "added" Change for the first appearance.
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1", len(feed))
	}
	if feed[0].ChangeType != domain.ChangeTypeAdded {
		t.Errorf("ChangeType = %q, want added", feed[0].ChangeType)
	}
	if feed[0].NewValue == nil || *feed[0].NewValue != "1.0.0" {
		t.Errorf("NewValue = %v, want 1.0.0", feed[0].NewValue)
	}
}

// buildThreeCommitRepo creates a repo with three commits to the same file.
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
			t.Fatalf("write file: %v", err)
		}
		if _, err := wt.Add("Chart.yaml"); err != nil {
			t.Fatalf("git add: %v", err)
		}
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "dev", Email: "d@x.com", When: when},
		})
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		return h.String()
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	sha1 = commit("1.0.0", "init", base)
	sha2 = commit("1.1.0", "bump", base.Add(time.Hour))
	sha3 = commit("1.2.0", "bump2", base.Add(2*time.Hour))

	return dir, sha1, sha2, sha3
}

func TestPoller_ResumesFromHighWaterMark(t *testing.T) {
	t.Parallel()

	repoPath, sha1, _, _ := buildThreeCommitRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)

	// Pre-seed the high-water mark to sha1, simulating a prior run that stopped there.
	if err := st.SetHighWaterMark(repoPath, sha1); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  "",
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll (resume): %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	// Should have two changes: sha2 (1.0.0→1.1.0) and sha3 (1.1.0→1.2.0).
	if len(feed) != 2 {
		t.Fatalf("got %d changes, want 2", len(feed))
	}
	// Newest first — sha3 change comes first.
	if feed[0].NewValue == nil || *feed[0].NewValue != "1.2.0" {
		t.Errorf("feed[0].NewValue = %v, want 1.2.0", feed[0].NewValue)
	}
	if feed[1].NewValue == nil || *feed[1].NewValue != "1.1.0" {
		t.Errorf("feed[1].NewValue = %v, want 1.1.0", feed[1].NewValue)
	}
}
