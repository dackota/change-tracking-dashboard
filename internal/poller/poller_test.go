package poller_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/poller"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ptr is a test-local helper to take the address of a string literal.
func ptr(s string) *string { return &s }

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
		BackfillDays:  3650, // 10 years — fixture commits are well within range
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
		BackfillDays:  3650,
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
		BackfillDays:  3650,
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
	if err := st.SetHighWaterMark(repoPath, "Chart.yaml", sha1); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  "",
		BackfillDays:  3650,
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

// buildDatedCommitRepo creates a repo with three commits at known dates:
// sha1 = 2024-01-01, sha2 = 2024-01-10, sha3 = 2024-01-20.
func buildDatedCommitRepo(t *testing.T) (repoPath, sha1, sha2, sha3 string) {
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
			t.Fatalf("commit %q: %v", msg, err)
		}
		return h.String()
	}

	sha1 = commit("1.0.0", "init", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	sha2 = commit("1.1.0", "bump1", time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC))
	sha3 = commit("1.2.0", "bump2", time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC))

	return dir, sha1, sha2, sha3
}

// TestPoller_FirstRun_BackfillWindowExcludesOldCommits verifies that on the
// first run (HWM empty), only commits within BackfillDays of the injected
// reference time are walked.
func TestPoller_FirstRun_BackfillWindowExcludesOldCommits(t *testing.T) {
	t.Parallel()

	// Repo has commits on Jan 1, Jan 10, Jan 20 2024.
	// Reference "now" = Jan 15 2024. BackfillDays = 7.
	// notBefore = Jan 8 2024 → sha2 (Jan 10) and sha3 (Jan 20) are in window;
	// sha1 (Jan 1) is excluded.
	repoPath, _, _, sha3 := buildDatedCommitRepo(t)

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
		BackfillDays:  7,
	}

	// Inject a fixed "now" so the backfill window is deterministic.
	refNow := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	p := poller.New(src, st).WithNow(func() time.Time { return refNow })

	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	// sha2 (Jan 10) is the oldest commit within the window; it becomes the
	// baseline "old" state. sha3 (Jan 20) is diffed against sha2, producing
	// one modified Change (1.1.0→1.2.0). sha1 (Jan 1) is outside the window
	// and is not walked at all.
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1 (sha2 is baseline, sha3 is diff target)", len(feed))
	}

	c := feed[0]
	if c.ChangeType != domain.ChangeTypeModified {
		t.Errorf("ChangeType = %q, want modified", c.ChangeType)
	}
	if c.OldValue == nil || *c.OldValue != "1.1.0" {
		t.Errorf("OldValue = %v, want 1.1.0 (sha2 baseline)", c.OldValue)
	}
	if c.NewValue == nil || *c.NewValue != "1.2.0" {
		t.Errorf("NewValue = %v, want 1.2.0 (sha3)", c.NewValue)
	}

	// HWM should be sha3 (the last commit walked).
	hwm, err := st.GetHighWaterMark(repoPath, "Chart.yaml")
	if err != nil {
		t.Fatalf("GetHighWaterMark: %v", err)
	}
	if hwm != sha3 {
		t.Errorf("HWM = %q, want sha3=%q", hwm, sha3)
	}
}

// TestPoller_IncrementalRun_UnaffectedByBackfillWindow verifies that on an
// incremental run (HWM set), the backfill window is NOT applied — the HWM alone
// bounds the walk.
//
// This fixture is deliberately *discriminating*: the backfill cutoff is placed
// BETWEEN the two new commits. With refNow = Jan 25 and BackfillDays = 10,
// notBefore = Jan 15 — which sits between sha2 (Jan 10) and sha3 (Jan 20).
// Correct behavior ignores the window on incremental runs, so BOTH sha2 and sha3
// are processed → two changes (including sha2's 1.0.0→1.1.0). If the window were
// wrongly applied here, sha2 (before the cutoff) would be filtered out and only a
// single sha1→sha3 change would appear — so this test FAILS if that bug regresses.
func TestPoller_IncrementalRun_UnaffectedByBackfillWindow(t *testing.T) {
	t.Parallel()

	repoPath, sha1, _, sha3 := buildDatedCommitRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)
	if err := st.SetHighWaterMark(repoPath, "Chart.yaml", sha1); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  "",
		BackfillDays:  10, // cutoff (Jan 15) lands BETWEEN sha2 (Jan 10) and sha3 (Jan 20)
	}

	refNow := time.Date(2024, 1, 25, 0, 0, 0, 0, time.UTC)
	p := poller.New(src, st).WithNow(func() time.Time { return refNow })

	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	// Correct: 2 changes. A wrongly-applied window would drop sha2 → 1 change.
	if len(feed) != 2 {
		t.Fatalf("got %d changes, want 2 — sha2 (before the backfill cutoff) must still be processed on an incremental run", len(feed))
	}

	// The pre-cutoff commit's change must be present. Feed is newest-first, so
	// feed[1] is sha2 (1.0.0→1.1.0); this is exactly what disappears if the
	// backfill window leaks into the incremental walk.
	got := feed[1]
	if got.OldValue == nil || *got.OldValue != "1.0.0" || got.NewValue == nil || *got.NewValue != "1.1.0" {
		t.Errorf("feed[1] = %v→%v, want 1.0.0→1.1.0 (sha2, dated before the cutoff)", got.OldValue, got.NewValue)
	}

	hwm, err := st.GetHighWaterMark(repoPath, "Chart.yaml")
	if err != nil {
		t.Fatalf("GetHighWaterMark: %v", err)
	}
	if hwm != sha3 {
		t.Errorf("HWM = %q, want sha3=%q", hwm, sha3)
	}
}

// TestPoller_HWMContentLookup_WorksForOutOfWindowHWM verifies that the HWM
// commit content lookup always uses an unbounded walk, so even if the HWM
// commit is older than the backfill window, the diff computation is correct.
func TestPoller_HWMContentLookup_WorksForOutOfWindowHWM(t *testing.T) {
	t.Parallel()

	// Repo has commits on Jan 1 (sha1), Jan 10 (sha2), Jan 20 (sha3).
	// HWM = sha1 (Jan 1). BackfillDays = 7, refNow = Jan 15.
	// sha1 is out of the backfill window. The incremental walk (since sha1)
	// returns sha2+sha3. The HWM lookup must find sha1's content unboundedly.
	repoPath, sha1, _, sha3 := buildDatedCommitRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)
	if err := st.SetHighWaterMark(repoPath, "Chart.yaml", sha1); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  "",
		BackfillDays:  7,
	}

	refNow := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	p := poller.New(src, st).WithNow(func() time.Time { return refNow })

	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	// Must produce 2 changes (sha1→sha2: 1.0.0→1.1.0, sha2→sha3: 1.1.0→1.2.0).
	if len(feed) != 2 {
		t.Fatalf("got %d changes, want 2", len(feed))
	}

	hwm, err := st.GetHighWaterMark(repoPath, "Chart.yaml")
	if err != nil {
		t.Fatalf("GetHighWaterMark: %v", err)
	}
	if hwm != sha3 {
		t.Errorf("HWM = %q, want sha3=%q", hwm, sha3)
	}

	// Oldest change should be 1.0.0→1.1.0 (diff from sha1's content to sha2).
	// feed is newest-first so feed[1] is the older change.
	if feed[1].OldValue == nil || *feed[1].OldValue != "1.0.0" {
		t.Errorf("feed[1].OldValue = %v, want 1.0.0 (from sha1 HWM content)", feed[1].OldValue)
	}
	if feed[1].NewValue == nil || *feed[1].NewValue != "1.1.0" {
		t.Errorf("feed[1].NewValue = %v, want 1.1.0", feed[1].NewValue)
	}
}

// buildKeyedFixtureRepo creates a fixture repo whose Chart.yaml dependencies
// change across two commits:
//   - commit 1: gateway@0.38.0, engine@1.0.0 (both with aliases)
//   - commit 2: gateway@0.39.0 (bumped), engine removed, analytics@2.0.0 added
//
// This exercises the mixed add/remove/modify case end-to-end through the poller.
func buildKeyedFixtureRepo(t *testing.T) (repoPath string) {
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
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	commit := func(content, msg string, when time.Time) {
		if err := os.WriteFile(chartPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		if _, err := wt.Add("Chart.yaml"); err != nil {
			t.Fatalf("git add: %v", err)
		}
		if _, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "dev", Email: "d@x.com", When: when},
		}); err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
	}

	commit(`apiVersion: v2
name: aidp
dependencies:
  - name: kanpai-gateway
    alias: aidp-gateway
    version: "0.38.0"
    repository: oci://registry.example.com
  - name: kanpai-engine
    alias: aidp-engine
    version: "1.0.0"
    repository: oci://registry.example.com
`, "init", base)

	commit(`apiVersion: v2
name: aidp
dependencies:
  - name: kanpai-gateway
    alias: aidp-gateway
    version: "0.39.0"
    repository: oci://registry.example.com
  - name: kanpai-analytics
    alias: aidp-analytics
    version: "2.0.0"
    repository: oci://registry.example.com
`, "bump gateway, remove engine, add analytics", base.Add(time.Hour))

	return dir
}

// TestPoller_KeyedEndToEnd confirms that the poller correctly processes a
// Chart.yaml whose dependencies change between two commits, producing per-key
// Changes with non-nil Key values persisted and queryable.
func TestPoller_KeyedEndToEnd(t *testing.T) {
	t.Parallel()

	repoPath := buildKeyedFixtureRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:     repoPath,
		FileGlob: "Chart.yaml",
		Field:    "subchart-versions",
		// alias-vs-name keying: prefer alias when present, else name.
		ExtractorExpr: `.dependencies | map({(if .alias then .alias else .name end): .version}) | add`,
		FacetPattern:  "",
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	// Expect exactly 3 Changes: aidp-gateway modified, aidp-engine removed,
	// aidp-analytics added.
	if len(feed) != 3 {
		t.Fatalf("got %d changes, want 3; feed = %+v", len(feed), feed)
	}

	// Index by key for order-independent assertions.
	byKey := make(map[string]domain.Change, len(feed))
	for _, c := range feed {
		if c.Key == nil {
			t.Errorf("keyed Change has nil Key: %+v", c)
			continue
		}
		byKey[*c.Key] = c
	}

	// aidp-gateway: modified 0.38.0 → 0.39.0
	gw, ok := byKey["aidp-gateway"]
	if !ok {
		t.Error("missing Change for key aidp-gateway")
	} else {
		if gw.ChangeType != domain.ChangeTypeModified {
			t.Errorf("aidp-gateway: ChangeType = %q, want modified", gw.ChangeType)
		}
		if gw.OldValue == nil || *gw.OldValue != "0.38.0" {
			t.Errorf("aidp-gateway: OldValue = %v, want 0.38.0", gw.OldValue)
		}
		if gw.NewValue == nil || *gw.NewValue != "0.39.0" {
			t.Errorf("aidp-gateway: NewValue = %v, want 0.39.0", gw.NewValue)
		}
		if gw.Field != "subchart-versions" {
			t.Errorf("aidp-gateway: Field = %q, want subchart-versions", gw.Field)
		}
	}

	// aidp-engine: removed
	eng, ok := byKey["aidp-engine"]
	if !ok {
		t.Error("missing Change for key aidp-engine")
	} else {
		if eng.ChangeType != domain.ChangeTypeRemoved {
			t.Errorf("aidp-engine: ChangeType = %q, want removed", eng.ChangeType)
		}
		if eng.OldValue == nil || *eng.OldValue != "1.0.0" {
			t.Errorf("aidp-engine: OldValue = %v, want 1.0.0", eng.OldValue)
		}
		if eng.NewValue != nil {
			t.Errorf("aidp-engine: NewValue = %v, want nil", eng.NewValue)
		}
	}

	// aidp-analytics: added
	an, ok := byKey["aidp-analytics"]
	if !ok {
		t.Error("missing Change for key aidp-analytics")
	} else {
		if an.ChangeType != domain.ChangeTypeAdded {
			t.Errorf("aidp-analytics: ChangeType = %q, want added", an.ChangeType)
		}
		if an.OldValue != nil {
			t.Errorf("aidp-analytics: OldValue = %v, want nil", an.OldValue)
		}
		if an.NewValue == nil || *an.NewValue != "2.0.0" {
			t.Errorf("aidp-analytics: NewValue = %v, want 2.0.0", an.NewValue)
		}
	}
}

// --- Engine selector tests ---

// TestPoller_EngineJQ_BehavesIdenticallyToUnset verifies that an explicit
// Engine: "jq" tracker produces the exact same Changes as an unset Engine —
// the FieldExtractor seam must not change today's default behavior.
func TestPoller_EngineJQ_BehavesIdenticallyToUnset(t *testing.T) {
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
		Engine:        "jq",
		FacetPattern:  `^apps/(?P<tenant>[^/]+)/(?P<env>[^/]+)/(?P<region>[^/]+)/`,
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("QueryFeed after poll: got %d changes, want 1", len(feed))
	}
	if feed[0].OldValue == nil || *feed[0].OldValue != "1.0.0" || feed[0].NewValue == nil || *feed[0].NewValue != "1.1.0" {
		t.Errorf("change = %v -> %v, want 1.0.0 -> 1.1.0", feed[0].OldValue, feed[0].NewValue)
	}
}

// TestPoller_UnrecognizedEngine_ReturnsError verifies Poll rejects a tracker
// carrying an unrecognized Engine value rather than silently defaulting or
// panicking. Config load is the primary guard, but the poller must not trust
// a Tracker value blindly — defense in depth through the same seam.
func TestPoller_UnrecognizedEngine_ReturnsError(t *testing.T) {
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
		Engine:        "hcl",
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err == nil {
		t.Fatal("Poll should have returned an error for an unrecognized engine, got nil")
	}
}

// --- Glob fan-out tests ---

// buildGlobFanOutRepo creates a fixture repo with two files matching the glob
// "a/*/Chart.yaml" (a/x/Chart.yaml, a/y/Chart.yaml) plus one non-matching file
// (b/Chart.yaml). Each matching file gets two commits so the poller's
// add-then-modify pipeline is exercised per file.
func buildGlobFanOutRepo(t *testing.T) (repoPath string) {
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

	writeAndCommit := func(relPath, content, msg string, when time.Time) string {
		full := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %q: %v", relPath, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %q: %v", relPath, err)
		}
		if _, err := wt.Add(relPath); err != nil {
			t.Fatalf("git add %q: %v", relPath, err)
		}
		h, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "dev", Email: "d@x.com", When: when},
		})
		if err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
		return h.String()
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// a/x/Chart.yaml: 1.0.0 -> 1.1.0
	writeAndCommit("a/x/Chart.yaml", "version: \"1.0.0\"\n", "x init", base)
	writeAndCommit("a/x/Chart.yaml", "version: \"1.1.0\"\n", "x bump", base.Add(time.Hour))

	// a/y/Chart.yaml: 2.0.0 -> 2.1.0
	writeAndCommit("a/y/Chart.yaml", "version: \"2.0.0\"\n", "y init", base.Add(2*time.Hour))
	writeAndCommit("a/y/Chart.yaml", "version: \"2.1.0\"\n", "y bump", base.Add(3*time.Hour))

	// b/Chart.yaml: does NOT match a/*/Chart.yaml — must be ignored entirely.
	writeAndCommit("b/Chart.yaml", "version: \"9.9.9\"\n", "b init", base.Add(4*time.Hour))

	return dir
}

// TestPoller_GlobFanOut_EmitsChangesPerMatchedFile verifies that a wildcard
// FileGlob is expanded across the repo tree, and the existing
// Extractor->Differ pipeline runs independently for each matching file, with
// facets derived from each file's own path. The non-matching file must produce
// no Changes at all.
func TestPoller_GlobFanOut_EmitsChangesPerMatchedFile(t *testing.T) {
	t.Parallel()

	repoPath := buildGlobFanOutRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "a/*/Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  `^a/(?P<app>[^/]+)/Chart\.yaml$`,
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	feed, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	// Each matched file has 2 commits -> one modified Change per file (1.0.0->1.1.0
	// and 2.0.0->2.1.0). The non-matching b/Chart.yaml must contribute nothing.
	if len(feed) != 2 {
		t.Fatalf("got %d changes, want 2 (one per matched file); feed = %+v", len(feed), feed)
	}

	byApp := make(map[string]domain.Change, len(feed))
	for _, c := range feed {
		byApp[c.Facets["app"]] = c
	}

	x, ok := byApp["x"]
	if !ok {
		t.Fatalf("missing Change for app=x; feed = %+v", feed)
	}
	if x.FilePath != "a/x/Chart.yaml" {
		t.Errorf("x.FilePath = %q, want a/x/Chart.yaml", x.FilePath)
	}
	if x.OldValue == nil || *x.OldValue != "1.0.0" {
		t.Errorf("x.OldValue = %v, want 1.0.0", x.OldValue)
	}
	if x.NewValue == nil || *x.NewValue != "1.1.0" {
		t.Errorf("x.NewValue = %v, want 1.1.0", x.NewValue)
	}

	y, ok := byApp["y"]
	if !ok {
		t.Fatalf("missing Change for app=y; feed = %+v", feed)
	}
	if y.FilePath != "a/y/Chart.yaml" {
		t.Errorf("y.FilePath = %q, want a/y/Chart.yaml", y.FilePath)
	}
	if y.OldValue == nil || *y.OldValue != "2.0.0" {
		t.Errorf("y.OldValue = %v, want 2.0.0", y.OldValue)
	}
	if y.NewValue == nil || *y.NewValue != "2.1.0" {
		t.Errorf("y.NewValue = %v, want 2.1.0", y.NewValue)
	}

	// The non-matching file must never appear.
	for _, c := range feed {
		if c.FilePath == "b/Chart.yaml" {
			t.Errorf("non-matching file b/Chart.yaml produced a Change: %+v", c)
		}
	}
}

// TestPoller_GlobFanOut_ResumePerFile verifies the critical per-file HWM
// correctness property: after a first poll fans out across matched files, a
// second poll with a NEW commit on only ONE matched file emits exactly that
// file's new Change — the other matched file's already-processed history is
// not recomputed, and the two files' resume cursors never clobber each other.
func TestPoller_GlobFanOut_ResumePerFile(t *testing.T) {
	t.Parallel()

	repoPath := buildGlobFanOutRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "a/*/Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version",
		FacetPattern:  `^a/(?P<app>[^/]+)/Chart\.yaml$`,
		BackfillDays:  3650,
	}

	p := poller.New(src, st)

	// First poll: establishes both files' baselines (2 changes, as above).
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll (first): %v", err)
	}
	feedAfterFirst, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed (first): %v", err)
	}
	if len(feedAfterFirst) != 2 {
		t.Fatalf("after first poll: got %d changes, want 2", len(feedAfterFirst))
	}

	// Add a new commit to ONLY a/x/Chart.yaml.
	xPath := filepath.Join(repoPath, "a", "x", "Chart.yaml")
	if err := os.WriteFile(xPath, []byte("version: \"1.2.0\"\n"), 0o644); err != nil {
		t.Fatalf("write x bump2: %v", err)
	}
	wt, err := func() (*git.Worktree, error) {
		r, err := git.PlainOpen(repoPath)
		if err != nil {
			return nil, err
		}
		return r.Worktree()
	}()
	if err != nil {
		t.Fatalf("worktree for second commit: %v", err)
	}
	if _, err := wt.Add("a/x/Chart.yaml"); err != nil {
		t.Fatalf("git add x bump2: %v", err)
	}
	if _, err := wt.Commit("x bump2", &git.CommitOptions{
		Author: &object.Signature{Name: "dev", Email: "d@x.com",
			When: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("commit x bump2: %v", err)
	}

	// Second poll: must only emit a/x/Chart.yaml's new change (1.1.0->1.2.0).
	// a/y/Chart.yaml has no new commits and must not be reprocessed at all —
	// proving its HWM was not clobbered by a/x/Chart.yaml's advance.
	if err := p.Poll(tracker); err != nil {
		t.Fatalf("Poll (second): %v", err)
	}

	feedAfterSecond, err := st.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed (second): %v", err)
	}
	if len(feedAfterSecond) != 3 {
		t.Fatalf("after second poll: got %d changes, want 3 (2 baseline + 1 new for x)", len(feedAfterSecond))
	}

	var newChanges []domain.Change
	for _, c := range feedAfterSecond {
		if c.OldValue != nil && *c.OldValue == "1.1.0" {
			newChanges = append(newChanges, c)
		}
	}
	if len(newChanges) != 1 {
		t.Fatalf("expected exactly 1 new change (x: 1.1.0->1.2.0), got %d: %+v", len(newChanges), newChanges)
	}
	nc := newChanges[0]
	if nc.FilePath != "a/x/Chart.yaml" {
		t.Errorf("new change FilePath = %q, want a/x/Chart.yaml", nc.FilePath)
	}
	if nc.NewValue == nil || *nc.NewValue != "1.2.0" {
		t.Errorf("new change NewValue = %v, want 1.2.0", nc.NewValue)
	}
	if nc.Facets["app"] != "x" {
		t.Errorf("new change facet app = %q, want x", nc.Facets["app"])
	}

	// y's HWM must be untouched: GetHighWaterMark(repo, a/y/Chart.yaml) still
	// resolves to its own last-processed commit, independent of x's advance.
	hwmY, err := st.GetHighWaterMark(repoPath, "a/y/Chart.yaml")
	if err != nil {
		t.Fatalf("GetHighWaterMark (y): %v", err)
	}
	if hwmY == "" {
		t.Error("HWM for a/y/Chart.yaml is empty after polls — per-file HWM not being set")
	}

	hwmX, err := st.GetHighWaterMark(repoPath, "a/x/Chart.yaml")
	if err != nil {
		t.Fatalf("GetHighWaterMark (x): %v", err)
	}
	if hwmX == hwmY {
		t.Errorf("HWM for x (%q) and y (%q) must differ — they are independent per-file cursors", hwmX, hwmY)
	}
}
