// poller_subject_test.go proves the commit-subject acceptance criteria
// (#85) end-to-end through the same seam every other poller feature is
// proven through — Poller.Poll against a fixture git repo, read back via the
// real store. Reuses initTFRepo/writeAndCommitTF/versionsTFContent/tfBase
// from poller_hcl_test.go (same package).
package poller_test

import (
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/poller"
)

// TestPoller_PersistsCommitSubject proves the commit message's first line is
// persisted as the Change's Subject.
func TestPoller_PersistsCommitSubject(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.5.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.10", ">= 1.5.0"), "bump google provider to 5.10.0", tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "versions.tf",
		Field:         "google-provider-version",
		ExtractorExpr: "terraform.required_providers.google.version",
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
		t.Fatalf("got %d changes, want 1; feed = %+v", len(feed), feed)
	}
	if got, want := feed[0].Subject, "bump google provider to 5.10.0"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
}

// TestPoller_MultiLineCommitMessage_PersistsOnlyFirstLineAsSubject proves
// that a multi-line commit message's Subject is just the first line — the
// body is discarded, matching the "subject vs full message" design decision
// in #85 (store just the subject for the feed).
func TestPoller_MultiLineCommitMessage_PersistsOnlyFirstLineAsSubject(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.5.0"), "init", tfBase)
	multiLineMsg := "bump google provider to 5.10.0\n\nThis picks up e2-standard-4 support and a\nsecurity fix for the metadata endpoint."
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.10", ">= 1.5.0"), multiLineMsg, tfBase.Add(time.Hour))

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}
	st := newTestStore(t)

	tracker := domain.Tracker{
		Repo:          repoPath,
		FileGlob:      "versions.tf",
		Field:         "google-provider-version",
		ExtractorExpr: "terraform.required_providers.google.version",
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
		t.Fatalf("got %d changes, want 1; feed = %+v", len(feed), feed)
	}
	if got, want := feed[0].Subject, "bump google provider to 5.10.0"; got != want {
		t.Errorf("Subject = %q, want %q (body must be discarded)", got, want)
	}
}
