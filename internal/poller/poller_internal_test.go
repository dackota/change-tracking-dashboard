// poller_internal_test.go is a white-box test (package poller, not
// poller_test) that exercises pollFileGroup directly to prove the FieldExtractor
// seam: pollFileGroup's extractor parameter is typed as the extractor.FieldExtractor
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

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/facet"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// fakeFieldExtractor is a test double satisfying extractor.Selection without
// running any gojq logic. Its Extract result is unreachable from the real file
// content ("from-fake" is not derivable from the committed YAML), so a
// persisted Change carrying it proves pollFileGroup used the injected
// extractor rather than constructing its own gojq-based one internally.
//
// It reports its own engine name for the same reason a real Selection does:
// the engine is part of what an extractor is, not a fact the poller tracks
// separately, so a substituted backend names itself in logs and metrics
// without the poller knowing anything about it.
type fakeFieldExtractor struct {
	engine string
	field  domain.TrackedField
}

func (f *fakeFieldExtractor) Engine() string { return f.engine }

func (f *fakeFieldExtractor) Extract(_ []byte) (domain.TrackedField, error) {
	return f.field, nil
}

// buildSingleFileRepo creates a minimal one-file, one-commit repo. The
// extraction expression / actual content are irrelevant here — this test is
// about which extractor pollFileGroup calls, not what a real one would produce.
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
// pollFileGroup is handed a fake extractor (not *extractor.Extractor), and the
// persisted Change reflects the fake's output — never a real jq evaluation
// of the file content.
func TestPollFileGroup_UsesInjectedFieldExtractor(t *testing.T) {
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

	fake := &fakeFieldExtractor{engine: "fake", field: domain.TrackedField{Value: "from-fake", Present: true}}

	tracker := domain.Tracker{
		Repo:         repoPath,
		FileGlob:     "Chart.yaml",
		Field:        "test-field",
		BackfillDays: 3650,
	}

	p := New(src, st)
	member := groupMember{tracker: tracker, ex: fake, fe: fe}
	errs := make([]error, 1)
	p.pollFileGroup(context.Background(), p.logger, "Chart.yaml", []groupMember{member}, errs)
	if errs[0] != nil {
		t.Fatalf("pollFileGroup: %v", errs[0])
	}

	// Read the persisted Changes back through the query path production
	// actually uses. An unbounded-below window ending in the future covers
	// every commit the fixture repo can produce.
	page, err := st.QueryChangesets(store.TimeWindow{AsOf: time.Now().Add(time.Hour)}, filter.FilterSpec{}, nil, "", 10)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}
	var feed []changeset.Change
	for _, cs := range page.Changesets {
		feed = append(feed, cs.Changes...)
	}
	if len(feed) != 1 {
		t.Fatalf("got %d changes, want 1", len(feed))
	}
	if feed[0].NewValue == nil || *feed[0].NewValue != "from-fake" {
		t.Errorf("NewValue = %v, want %q (from the fake extractor — the poll path must use the injected FieldExtractor)",
			feed[0].NewValue, "from-fake")
	}
}
