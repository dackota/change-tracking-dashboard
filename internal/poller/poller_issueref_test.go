// poller_issueref_test.go proves the tf-issue-correlation acceptance
// criteria end-to-end through the same seam every other engine/feature is
// proven through — Poller.Poll against a fixture git repo, read back via the
// real store — never the issueref package's regex internals directly.
// Reuses initTFRepo/writeAndCommitTF/versionsTFContent/tfBase from
// poller_hcl_test.go (same package).
package poller_test

import (
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/poller"
)

// TestPoller_CommitMessageWithGitHubStyleRef_LinksChangeToItsIssue proves the
// first acceptance criterion: a commit whose message contains a bare-numeric
// GitHub-style reference (#123) yields a Changeset (here, its constituent
// Change) linked to that issue.
func TestPoller_CommitMessageWithGitHubStyleRef_LinksChangeToItsIssue(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.5.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.10", ">= 1.5.0"), "Fixes #123: bump google provider", tfBase.Add(time.Hour))

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
	if got := feed[0].IssueRefs; len(got) != 1 || got[0] != "#123" {
		t.Errorf("IssueRefs = %#v, want [\"#123\"]", got)
	}
}

// TestPoller_CommitMessageWithNoRef_YieldsNoLink proves the second
// acceptance criterion: a commit with no issue reference yields a Change
// with no link — no false positives.
func TestPoller_CommitMessageWithNoRef_YieldsNoLink(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.5.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.10", ">= 1.5.0"), "bump google provider to pick up e2-standard-4 support", tfBase.Add(time.Hour))

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
	if got := feed[0].IssueRefs; len(got) != 0 {
		t.Errorf("IssueRefs = %#v, want empty (no false link)", got)
	}
}

// TestPoller_CommitMessageWithMultipleRefs_LinksToAll proves the third
// acceptance criterion: multiple references in one commit message link to
// all of them.
func TestPoller_CommitMessageWithMultipleRefs_LinksToAll(t *testing.T) {
	t.Parallel()

	repoPath := initTFRepo(t)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.0", ">= 1.5.0"), "init", tfBase)
	writeAndCommitTF(t, repoPath, "versions.tf", versionsTFContent("~> 5.10", ">= 1.5.0"), "Fixes #123 and ABC-456: bump google provider", tfBase.Add(time.Hour))

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
	if got := feed[0].IssueRefs; len(got) != 2 || got[0] != "#123" || got[1] != "ABC-456" {
		t.Errorf("IssueRefs = %#v, want [\"#123\", \"ABC-456\"]", got)
	}
}
