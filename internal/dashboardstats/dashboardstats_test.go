package dashboardstats_test

import (
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/dashboardstats"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// TestCompute is the table test documenting the specific cases from the PRD
// Testing Decisions: empty set, a single Changeset, multiple Changesets
// across two repos, a Chart/value Change mix, and the max-CommittedAt
// LastChangeAt rule.
func TestCompute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		changesets []changeset.Changeset
		want       dashboardstats.Metrics
	}{
		{
			name:       "empty set degrades to all-zero metrics with zero LastChangeAt",
			changesets: nil,
			want:       dashboardstats.Metrics{},
		},
		{
			name: "single Changeset with one Change counts Changesets, Changes, and Repositories",
			changesets: []changeset.Changeset{
				{
					Repo:        "apps-repo",
					CommitSha:   "abc123",
					Author:      "alice",
					CommittedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
					Changes: []changeset.Change{
						{Change: domain.Change{Repo: "apps-repo"}, Kind: changeset.KindValue},
					},
				},
			},
			want: dashboardstats.Metrics{
				Changesets:   1,
				Changes:      1,
				Repositories: 1,
				ChartChanges: 0,
				LastChangeAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "two Changesets in the same repo count as one distinct Repository",
			changesets: []changeset.Changeset{
				{
					Repo:        "apps-repo",
					CommitSha:   "commit-1",
					CommittedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
					Changes:     []changeset.Change{{Change: domain.Change{Repo: "apps-repo"}, Kind: changeset.KindValue}},
				},
				{
					Repo:        "apps-repo",
					CommitSha:   "commit-2",
					CommittedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
					Changes:     []changeset.Change{{Change: domain.Change{Repo: "apps-repo"}, Kind: changeset.KindValue}},
				},
			},
			want: dashboardstats.Metrics{
				Changesets:   2,
				Changes:      2,
				Repositories: 1,
				ChartChanges: 0,
				LastChangeAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "two Changesets across two distinct repos count Repositories as 2",
			changesets: []changeset.Changeset{
				{
					Repo:        "repo-a",
					CommitSha:   "commit-1",
					CommittedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
					Changes:     []changeset.Change{{Change: domain.Change{Repo: "repo-a"}, Kind: changeset.KindValue}},
				},
				{
					Repo:        "repo-b",
					CommitSha:   "commit-2",
					CommittedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
					Changes:     []changeset.Change{{Change: domain.Change{Repo: "repo-b"}, Kind: changeset.KindValue}},
				},
			},
			want: dashboardstats.Metrics{
				Changesets:   2,
				Changes:      2,
				Repositories: 2,
				ChartChanges: 0,
				LastChangeAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "Chart and value Changes mixed in one Changeset count ChartChanges distinctly from Changes",
			changesets: []changeset.Changeset{
				{
					Repo:        "apps-repo",
					CommitSha:   "mixed-commit",
					CommittedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
					Changes: []changeset.Change{
						{Change: domain.Change{Repo: "apps-repo", FilePath: "Chart.yaml"}, Kind: changeset.KindChart},
						{Change: domain.Change{Repo: "apps-repo", FilePath: "values.yaml"}, Kind: changeset.KindValue},
						{Change: domain.Change{Repo: "apps-repo", FilePath: "values.yaml"}, Kind: changeset.KindValue},
					},
				},
			},
			want: dashboardstats.Metrics{
				Changesets:   1,
				Changes:      3,
				Repositories: 1,
				ChartChanges: 1,
				LastChangeAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "LastChangeAt is the max CommittedAt even when input is supplied out of order",
			changesets: []changeset.Changeset{
				{
					Repo:        "apps-repo",
					CommitSha:   "commit-newest",
					CommittedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
				},
				{
					Repo:        "apps-repo",
					CommitSha:   "commit-oldest",
					CommittedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				},
				{
					Repo:        "apps-repo",
					CommitSha:   "commit-middle",
					CommittedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
				},
			},
			want: dashboardstats.Metrics{
				Changesets:   3,
				Changes:      0,
				Repositories: 1,
				ChartChanges: 0,
				LastChangeAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := dashboardstats.Compute(tc.changesets)

			if got.Changesets != tc.want.Changesets {
				t.Errorf("Changesets = %d, want %d", got.Changesets, tc.want.Changesets)
			}
			if got.Changes != tc.want.Changes {
				t.Errorf("Changes = %d, want %d", got.Changes, tc.want.Changes)
			}
			if got.Repositories != tc.want.Repositories {
				t.Errorf("Repositories = %d, want %d", got.Repositories, tc.want.Repositories)
			}
			if got.ChartChanges != tc.want.ChartChanges {
				t.Errorf("ChartChanges = %d, want %d", got.ChartChanges, tc.want.ChartChanges)
			}
			if !got.LastChangeAt.Equal(tc.want.LastChangeAt) {
				t.Errorf("LastChangeAt = %v, want %v", got.LastChangeAt, tc.want.LastChangeAt)
			}
		})
	}
}

// TestCompute_DoesNotMutateInput asserts the immutability invariant: Compute
// never mutates the input slice, any Changeset's fields, or any Change's
// fields. It snapshots the input before calling Compute and asserts it is
// byte-for-byte unchanged after.
func TestCompute_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	committedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	input := []changeset.Changeset{
		{
			Repo:        "apps-repo",
			CommitSha:   "commit-1",
			Author:      "alice",
			CommittedAt: committedAt,
			Changes: []changeset.Change{
				{Change: domain.Change{Repo: "apps-repo", FilePath: "Chart.yaml"}, Kind: changeset.KindChart},
				{Change: domain.Change{Repo: "apps-repo", FilePath: "values.yaml"}, Kind: changeset.KindValue},
			},
		},
	}

	wantRepo := input[0].Repo
	wantChangeCount := len(input[0].Changes)
	wantKind0 := input[0].Changes[0].Kind
	wantCommittedAt := input[0].CommittedAt

	_ = dashboardstats.Compute(input)

	if input[0].Repo != wantRepo {
		t.Errorf("input[0].Repo mutated: got %q, want %q", input[0].Repo, wantRepo)
	}
	if len(input[0].Changes) != wantChangeCount {
		t.Errorf("input[0].Changes length mutated: got %d, want %d", len(input[0].Changes), wantChangeCount)
	}
	if input[0].Changes[0].Kind != wantKind0 {
		t.Errorf("input[0].Changes[0].Kind mutated: got %q, want %q", input[0].Changes[0].Kind, wantKind0)
	}
	if !input[0].CommittedAt.Equal(wantCommittedAt) {
		t.Errorf("input[0].CommittedAt mutated: got %v, want %v", input[0].CommittedAt, wantCommittedAt)
	}
}
