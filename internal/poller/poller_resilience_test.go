package poller_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/poller"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestPoller_GlobFanOut_ContinuesPastFailingFile is the invariant test for the
// per-file resilience fix: when a glob fans out across several files and ONE of
// them fails extraction, Poll must still process the remaining files (persisting
// their Changes) AND still surface the failure as a non-nil error — it must not
// abort the whole cycle on the first bad file.
//
// The failing file (a/bad/Chart.yaml) sorts BEFORE the good one
// (a/good/Chart.yaml), so MatchingFiles hands it to pollFile first: the
// pre-fix "return on first error" behavior would drop a/good entirely and this
// test's feed assertion would fail.
func TestPoller_GlobFanOut_ContinuesPastFailingFile(t *testing.T) {
	t.Parallel()

	repoPath := buildResilienceRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "a/*/Chart.yaml",
		Field:         "chart-version",
		ExtractorExpr: ".version", // errors on a/bad (scalar), succeeds on a/good (map)
		FacetPattern:  `^a/(?P<app>[^/]+)/Chart\.yaml$`,
		BackfillDays:  3650,
	}

	p := poller.New(src, st)
	err = p.Poll(tracker)

	// The failing file must still surface an error (never silently swallowed).
	if err == nil {
		t.Fatalf("Poll returned nil, want an error for the failing a/bad/Chart.yaml")
	}

	// ...but the good file's Change must have been persisted regardless.
	feed, ferr := st.QueryFeed(100)
	if ferr != nil {
		t.Fatalf("QueryFeed: %v", ferr)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1 (a/good only, despite a/bad failing); feed = %+v", len(feed), feed)
	}
	got := feed[0]
	if got.FilePath != "a/good/Chart.yaml" {
		t.Errorf("FilePath = %q, want a/good/Chart.yaml", got.FilePath)
	}
	if got.OldValue == nil || *got.OldValue != "1.0.0" || got.NewValue == nil || *got.NewValue != "1.1.0" {
		t.Errorf("change = %v -> %v, want 1.0.0 -> 1.1.0", got.OldValue, got.NewValue)
	}
}

// buildResilienceRepo builds a repo with two files matched by a/*/Chart.yaml:
// a/good (a well-formed version map that bumps 1.0.0 -> 1.1.0) and a/bad (a
// scalar, so the `.version` extractor errors on it).
func buildResilienceRepo(t *testing.T) (repoPath string) {
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

	writeAndCommit := func(relPath, content, msg string, when time.Time) {
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
		if _, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{Name: "dev", Email: "d@x.com", When: when},
		}); err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// a/good: a proper map that bumps -> yields one modified Change.
	writeAndCommit("a/good/Chart.yaml", "version: \"1.0.0\"\n", "good init", base)
	writeAndCommit("a/good/Chart.yaml", "version: \"1.1.0\"\n", "good bump", base.Add(time.Hour))
	// a/bad: a YAML scalar, so `.version` errors ("expected an object but got: string").
	writeAndCommit("a/bad/Chart.yaml", "not-a-map\n", "bad init", base.Add(2*time.Hour))

	return dir
}
