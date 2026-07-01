package changeset_test

import (
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
)

func ptr(s string) *string { return &s }

func TestAssemble(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		changes []domain.Change
		wantLen int
		// wantFirst describes the metadata of the first (or only) Changeset
		// returned, when wantLen > 0.
		wantRepo        string
		wantCommitSha   string
		wantAuthor      string
		wantCommittedAt time.Time
		wantChangeCount int
	}{
		{
			name: "single commit with one Change yields one Changeset carrying commit metadata",
			changes: []domain.Change{
				{
					Repo:        "apps-repo",
					FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
					Field:       "aidp-version",
					ChangeType:  domain.ChangeTypeModified,
					OldValue:    ptr("1.0.0"),
					NewValue:    ptr("1.1.0"),
					CommitSha:   "abc123",
					Author:      "alice",
					CommittedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
				},
			},
			wantLen:         1,
			wantRepo:        "apps-repo",
			wantCommitSha:   "abc123",
			wantAuthor:      "alice",
			wantCommittedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
			wantChangeCount: 1,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := changeset.Assemble(tc.changes)

			if len(got) != tc.wantLen {
				t.Fatalf("Assemble() returned %d Changesets, want %d", len(got), tc.wantLen)
			}
			if tc.wantLen == 0 {
				return
			}

			cs := got[0]
			if cs.Repo != tc.wantRepo {
				t.Errorf("Repo: got %q, want %q", cs.Repo, tc.wantRepo)
			}
			if cs.CommitSha != tc.wantCommitSha {
				t.Errorf("CommitSha: got %q, want %q", cs.CommitSha, tc.wantCommitSha)
			}
			if cs.Author != tc.wantAuthor {
				t.Errorf("Author: got %q, want %q", cs.Author, tc.wantAuthor)
			}
			if !cs.CommittedAt.Equal(tc.wantCommittedAt) {
				t.Errorf("CommittedAt: got %v, want %v", cs.CommittedAt, tc.wantCommittedAt)
			}
			if len(cs.Changes) != tc.wantChangeCount {
				t.Errorf("len(Changes): got %d, want %d", len(cs.Changes), tc.wantChangeCount)
			}
		})
	}
}

func TestAssemble_GroupsMultipleChangesFromSameCommitIntoOneChangeset(t *testing.T) {
	t.Parallel()

	commitTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	changes := []domain.Change{
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
			Field:       "image-tag-a",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("v1"),
			NewValue:    ptr("v2"),
			CommitSha:   "commit-1",
			Author:      "alice",
			CommittedAt: commitTime,
		},
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
			Field:       "image-tag-b",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("v3"),
			NewValue:    ptr("v4"),
			CommitSha:   "commit-1",
			Author:      "alice",
			CommittedAt: commitTime,
		},
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
			Field:       "image-tag-c",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("v5"),
			NewValue:    ptr("v6"),
			CommitSha:   "commit-1",
			Author:      "alice",
			CommittedAt: commitTime,
		},
	}

	got := changeset.Assemble(changes)

	if len(got) != 1 {
		t.Fatalf("Assemble() returned %d Changesets, want 1", len(got))
	}
	if len(got[0].Changes) != 3 {
		t.Fatalf("len(Changes): got %d, want 3", len(got[0].Changes))
	}
}

func TestAssemble_OrdersChangesetsMostRecentFirstByCommittedAt(t *testing.T) {
	t.Parallel()

	oldest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Deliberately supplied out of order to prove Assemble sorts, not
	// merely preserves input order.
	changes := []domain.Change{
		{Repo: "apps-repo", FilePath: "values.yaml", CommitSha: "commit-oldest", Author: "alice", CommittedAt: oldest, ChangeType: domain.ChangeTypeModified},
		{Repo: "apps-repo", FilePath: "values.yaml", CommitSha: "commit-newest", Author: "bob", CommittedAt: newest, ChangeType: domain.ChangeTypeModified},
		{Repo: "apps-repo", FilePath: "values.yaml", CommitSha: "commit-middle", Author: "carol", CommittedAt: middle, ChangeType: domain.ChangeTypeModified},
	}

	got := changeset.Assemble(changes)

	if len(got) != 3 {
		t.Fatalf("Assemble() returned %d Changesets, want 3", len(got))
	}

	wantOrder := []string{"commit-newest", "commit-middle", "commit-oldest"}
	for i, want := range wantOrder {
		if got[i].CommitSha != want {
			t.Errorf("Changeset[%d].CommitSha: got %q, want %q", i, got[i].CommitSha, want)
		}
	}
}

func TestAssemble_BreaksCommittedAtTiesByCommitShaForDeterminism(t *testing.T) {
	t.Parallel()

	sameTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	changes := []domain.Change{
		{Repo: "apps-repo", FilePath: "values.yaml", CommitSha: "zzz", Author: "alice", CommittedAt: sameTime, ChangeType: domain.ChangeTypeModified},
		{Repo: "apps-repo", FilePath: "values.yaml", CommitSha: "aaa", Author: "bob", CommittedAt: sameTime, ChangeType: domain.ChangeTypeModified},
	}

	got := changeset.Assemble(changes)

	if len(got) != 2 {
		t.Fatalf("Assemble() returned %d Changesets, want 2", len(got))
	}

	// Tie-break is stable/deterministic by CommitSha ascending.
	if got[0].CommitSha != "aaa" || got[1].CommitSha != "zzz" {
		t.Errorf("tie-break order: got [%q, %q], want [\"aaa\", \"zzz\"]", got[0].CommitSha, got[1].CommitSha)
	}
}

func TestAssemble_MixedCommitYieldsOneChangesetWithDifferingChangeKinds(t *testing.T) {
	t.Parallel()

	commitTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// One commit bumping both the chart version (Chart.yaml) and an image
	// tag (values.yaml) in the same commit.
	changes := []domain.Change{
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			Field:       "aidp-version",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("1.0.0"),
			NewValue:    ptr("1.1.0"),
			CommitSha:   "mixed-commit",
			Author:      "dana",
			CommittedAt: commitTime,
		},
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
			Field:       "image-tag",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("v1"),
			NewValue:    ptr("v2"),
			CommitSha:   "mixed-commit",
			Author:      "dana",
			CommittedAt: commitTime,
		},
	}

	got := changeset.Assemble(changes)

	if len(got) != 1 {
		t.Fatalf("Assemble() returned %d Changesets, want 1 (mixed commit must not split)", len(got))
	}

	cs := got[0]
	if len(cs.Changes) != 2 {
		t.Fatalf("len(Changes): got %d, want 2", len(cs.Changes))
	}

	gotKinds := map[changeset.Kind]int{}
	for _, c := range cs.Changes {
		gotKinds[c.Kind]++
	}
	if gotKinds[changeset.KindChart] != 1 {
		t.Errorf("KindChart count: got %d, want 1", gotKinds[changeset.KindChart])
	}
	if gotKinds[changeset.KindValue] != 1 {
		t.Errorf("KindValue count: got %d, want 1", gotKinds[changeset.KindValue])
	}
}

func TestAssemble_DoesNotMutateInputChangesOrTheirFacets(t *testing.T) {
	t.Parallel()

	commitTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	inputFacets := map[string]string{"tenant": "tenant-zero"}
	input := []domain.Change{
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
			Field:       "image-tag",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("v1"),
			NewValue:    ptr("v2"),
			Facets:      inputFacets,
			CommitSha:   "commit-1",
			Author:      "alice",
			CommittedAt: commitTime,
		},
	}

	// Snapshot the input before calling Assemble.
	wantFilePath := input[0].FilePath
	wantFacetsLen := len(input[0].Facets)

	got := changeset.Assemble(input)

	// Input slice/struct fields must be untouched.
	if input[0].FilePath != wantFilePath {
		t.Errorf("input FilePath mutated: got %q, want %q", input[0].FilePath, wantFilePath)
	}
	if len(input[0].Facets) != wantFacetsLen {
		t.Errorf("input Facets length mutated: got %d, want %d", len(input[0].Facets), wantFacetsLen)
	}

	// Mutating the returned Changeset's Change's Facets map must not affect
	// the original input map (defensive copy, not aliasing).
	if len(got) != 1 || len(got[0].Changes) != 1 {
		t.Fatalf("unexpected Assemble() shape: %+v", got)
	}
	got[0].Changes[0].Facets["injected"] = "should-not-leak-back"

	if _, present := inputFacets["injected"]; present {
		t.Error("mutating output Facets leaked back into input Facets map — Assemble must defensively copy")
	}
}

func TestAssemble_DoesNotMergeSameCommitShaAcrossDifferentRepos(t *testing.T) {
	t.Parallel()

	// A CommitSha is only unique within its own repo. Two different repos
	// coincidentally sharing a SHA value must not be merged into one
	// Changeset.
	commitTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	changes := []domain.Change{
		{Repo: "repo-a", FilePath: "values.yaml", CommitSha: "shared-sha", Author: "alice", CommittedAt: commitTime, ChangeType: domain.ChangeTypeModified},
		{Repo: "repo-b", FilePath: "values.yaml", CommitSha: "shared-sha", Author: "bob", CommittedAt: commitTime, ChangeType: domain.ChangeTypeModified},
	}

	got := changeset.Assemble(changes)

	if len(got) != 2 {
		t.Fatalf("Assemble() returned %d Changesets, want 2 (same SHA in different repos must not merge)", len(got))
	}

	gotRepos := map[string]bool{got[0].Repo: true, got[1].Repo: true}
	if !gotRepos["repo-a"] || !gotRepos["repo-b"] {
		t.Errorf("expected Changesets for both repo-a and repo-b, got repos: %v", gotRepos)
	}
}

func TestAssemble_EmptyInputYieldsEmptyOutput(t *testing.T) {
	t.Parallel()

	got := changeset.Assemble(nil)

	if len(got) != 0 {
		t.Errorf("Assemble(nil) returned %d Changesets, want 0", len(got))
	}
}
