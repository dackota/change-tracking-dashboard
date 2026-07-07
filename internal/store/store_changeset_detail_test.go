package store_test

import (
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// TestGetChangeset_ReturnsAllChangesForCommit verifies that GetChangeset
// looks up a single Changeset by (repo, commitSha) and returns every Change
// that commit produced — the detail view's "surface all Changes of that
// commit's Changeset" requirement (acceptance criterion 1) depends on this
// lookup returning the full set, not just one row.
func TestGetChangeset_ReturnsAllChangesForCommit(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	base := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)

	valueChange := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		CommitSha:   "commit-mixed",
		Author:      "alice",
		CommittedAt: base,
	}
	keyVal := "kanpai-gateway"
	chartChange := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "subchart-versions",
		Key:         &keyVal,
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("0.38.0"),
		NewValue:    ptr("0.39.0"),
		CommitSha:   "commit-mixed",
		Author:      "alice",
		CommittedAt: base,
	}
	otherCommit := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-one/prod/eu-west-1/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v3"),
		NewValue:    ptr("v4"),
		CommitSha:   "commit-other",
		Author:      "bob",
		CommittedAt: base.Add(time.Hour),
	}
	seedChanges(t, s, []domain.Change{valueChange, chartChange, otherCommit})

	cs, found, err := s.GetChangeset("apps-repo", "commit-mixed")
	if err != nil {
		t.Fatalf("GetChangeset: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if cs.CommitSha != "commit-mixed" {
		t.Errorf("CommitSha = %q, want commit-mixed", cs.CommitSha)
	}
	if len(cs.Changes) != 2 {
		t.Fatalf("Changes len = %d, want 2 (both Changes of commit-mixed)", len(cs.Changes))
	}
}

// TestGetChangeset_UnknownCommit_ReturnsNotFound verifies that looking up a
// (repo, commitSha) pair with no matching Changes reports found=false rather
// than an error — an unknown commit is a normal "nothing here" case, not a
// failure.
func TestGetChangeset_UnknownCommit_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	_, found, err := s.GetChangeset("apps-repo", "does-not-exist")
	if err != nil {
		t.Fatalf("GetChangeset: %v", err)
	}
	if found {
		t.Fatal("found = true, want false for an unknown commit")
	}
}
