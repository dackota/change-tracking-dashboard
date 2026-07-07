// poller_internal_test.go is a white-box test (package poller, not
// poller_test) that exercises pollFile directly to prove the FieldExtractor
// seam: pollFile's extractor parameter is typed as the extractor.FieldExtractor
// interface, so any implementation — not just the concrete gojq-based
// *extractor.Extractor — can be substituted. This is the property the
// prefactor exists to guarantee: an alternate backend (e.g. HCL, in a later
// task) can be wired in without touching poll/diff flow.
package poller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/facet"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// fakeFieldExtractor is a test double satisfying extractor.FieldExtractor
// without running any gojq logic. Its Extract result is unreachable from the
// real file content ("from-fake" is not derivable from the committed YAML),
// so a persisted Change carrying it proves pollFile used the injected
// extractor rather than constructing its own gojq-based one internally.
type fakeFieldExtractor struct {
	field domain.TrackedField
}

func (f *fakeFieldExtractor) Extract(_ []byte) (domain.TrackedField, error) {
	return f.field, nil
}

// buildSingleFileRepo creates a minimal one-file, one-commit repo. The
// extraction expression / actual content are irrelevant here — this test is
// about which extractor pollFile calls, not what a real one would produce.
func buildSingleFileRepo(t *testing.T) (repoPath string) {
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
	if err := os.WriteFile(chartPath, []byte("version: \"irrelevant\"\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := wt.Add("Chart.yaml"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "dev", Email: "d@x.com",
			When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return dir
}

// TestPollFile_UsesInjectedFieldExtractor proves the FieldExtractor seam:
// pollFile is handed a fake extractor (not *extractor.Extractor), and the
// persisted Change reflects the fake's output — never a real jq evaluation
// of the file content.
func TestPollFile_UsesInjectedFieldExtractor(t *testing.T) {
	repoPath := buildSingleFileRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "poller_internal_test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	fe, err := facet.NewExtractor("")
	if err != nil {
		t.Fatalf("facet.NewExtractor: %v", err)
	}

	fake := &fakeFieldExtractor{field: domain.TrackedField{Value: "from-fake", Present: true}}

	tracker := domain.Tracker{
		Repo:         repoPath,
		FileGlob:     "Chart.yaml",
		Field:        "test-field",
		BackfillDays: 3650,
	}

	p := New(src, st)
	if err := p.pollFile(context.Background(), p.logger, tracker, "Chart.yaml", fake, fe); err != nil {
		t.Fatalf("pollFile: %v", err)
	}

	feed, err := st.QueryFeed(10)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1", len(feed))
	}
	if feed[0].NewValue == nil || *feed[0].NewValue != "from-fake" {
		t.Errorf("NewValue = %v, want %q (from the fake extractor — pollFile must use the injected FieldExtractor)",
			feed[0].NewValue, "from-fake")
	}
}
