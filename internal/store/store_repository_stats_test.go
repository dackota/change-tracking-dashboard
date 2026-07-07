package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// repoStatsBase is the reference commit time for RepositoryStats tests.
var repoStatsBase = time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)

// TestRepositoryStats_AggregatesPerRepoAcrossChangeAndChartKinds verifies R4:
// RepositoryStats groups Changes by repo and reports, per repo, the total
// Change count, the count of chart-kind (basename Chart.yaml) Changes among
// them, and the most recent commit time — with value-kind Changes (any other
// file) counted toward ChangeCount but not ChartChanges.
func TestRepositoryStats_AggregatesPerRepoAcrossChangeAndChartKinds(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	appsValue1 := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		CommitSha:   "apps-sha-1",
		Author:      "alice",
		CommittedAt: repoStatsBase,
	}
	appsValue2 := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-one/prod/eu-west-1/values.yaml",
		Field:       "replica-count",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("2"),
		NewValue:    ptr("3"),
		CommitSha:   "apps-sha-2",
		Author:      "bob",
		CommittedAt: repoStatsBase.Add(time.Hour),
	}
	appsChart := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.0.0"),
		NewValue:    ptr("1.1.0"),
		CommitSha:   "apps-sha-3",
		Author:      "carol",
		CommittedAt: repoStatsBase.Add(2 * time.Hour), // most recent for apps-repo
	}
	infraChart := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "Chart.yaml",
		Field:       "version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("0.1.0"),
		NewValue:    ptr("0.2.0"),
		CommitSha:   "infra-sha-1",
		Author:      "dave",
		CommittedAt: repoStatsBase.Add(30 * time.Minute),
	}

	seedChanges(t, s, []domain.Change{appsValue1, appsValue2, appsChart, infraChart})

	got, err := s.RepositoryStats()
	if err != nil {
		t.Fatalf("RepositoryStats: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("RepositoryStats returned %d rows, want 2; got %#v", len(got), got)
	}

	byRepo := make(map[string]store.RepositoryStats, len(got))
	for _, rs := range got {
		byRepo[rs.Repo] = rs
	}

	apps, ok := byRepo["apps-repo"]
	if !ok {
		t.Fatalf("missing apps-repo in %#v", got)
	}
	if apps.ChangeCount != 3 {
		t.Errorf("apps-repo.ChangeCount = %d, want 3", apps.ChangeCount)
	}
	if apps.ChartChanges != 1 {
		t.Errorf("apps-repo.ChartChanges = %d, want 1", apps.ChartChanges)
	}
	if !apps.LastChangeAt.Equal(repoStatsBase.Add(2 * time.Hour)) {
		t.Errorf("apps-repo.LastChangeAt = %v, want %v", apps.LastChangeAt, repoStatsBase.Add(2*time.Hour))
	}

	infra, ok := byRepo["infra-repo"]
	if !ok {
		t.Fatalf("missing infra-repo in %#v", got)
	}
	if infra.ChangeCount != 1 {
		t.Errorf("infra-repo.ChangeCount = %d, want 1", infra.ChangeCount)
	}
	if infra.ChartChanges != 1 {
		t.Errorf("infra-repo.ChartChanges = %d, want 1", infra.ChartChanges)
	}
	if !infra.LastChangeAt.Equal(repoStatsBase.Add(30 * time.Minute)) {
		t.Errorf("infra-repo.LastChangeAt = %v, want %v", infra.LastChangeAt, repoStatsBase.Add(30*time.Minute))
	}
}

// TestRepositoryStats_DeterministicOrder verifies R4's deterministic-order
// contract: rows are returned sorted by Repo ascending, regardless of insert
// order — so the Repositories view (and any downstream repo filter) never
// depends on incidental row/poll ordering.
func TestRepositoryStats_DeterministicOrder(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Insert in reverse-alphabetical order to make the assertion meaningful.
	seedChanges(t, s, []domain.Change{
		{
			Repo: "zeta-repo", FilePath: "values.yaml", Field: "f",
			ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
			CommitSha: "z-1", Author: "a", CommittedAt: repoStatsBase,
		},
		{
			Repo: "alpha-repo", FilePath: "values.yaml", Field: "f",
			ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
			CommitSha: "a-1", Author: "a", CommittedAt: repoStatsBase,
		},
		{
			Repo: "mid-repo", FilePath: "values.yaml", Field: "f",
			ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
			CommitSha: "m-1", Author: "a", CommittedAt: repoStatsBase,
		},
	})

	got, err := s.RepositoryStats()
	if err != nil {
		t.Fatalf("RepositoryStats: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("RepositoryStats returned %d rows, want 3", len(got))
	}

	want := []string{"alpha-repo", "mid-repo", "zeta-repo"}
	for i, w := range want {
		if got[i].Repo != w {
			t.Errorf("got[%d].Repo = %q, want %q (order: %v)", i, got[i].Repo, w, got)
		}
	}
}

// TestRepositoryStats_EmptyDatabase_ReturnsEmptyNotNil verifies R4's
// empty-degrade contract: an empty database yields a non-nil, zero-length
// slice and no error, so the Repositories view's empty-state branch can be
// driven by length alone rather than a nil check.
func TestRepositoryStats_EmptyDatabase_ReturnsEmptyNotNil(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	got, err := s.RepositoryStats()
	if err != nil {
		t.Fatalf("RepositoryStats (empty): %v", err)
	}
	if got == nil {
		t.Fatal("RepositoryStats (empty) = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("RepositoryStats (empty) returned %d rows, want 0", len(got))
	}
}

// TestRepositoryStats_ChartChangesMatchesClassifyKindCaseSensitively is a
// class-level regression test for the chart_changes SQL predicate's
// case-sensitivity: it must agree with changeset.ClassifyKind's basename
// check (filepath.Base(filePath) == "Chart.yaml", a case-sensitive, binary
// comparison) for every file path in the class, not just the single
// previously-broken example. It seeds one Change per path across exact
// matches, case variations at the root and nested under a directory, and
// non-matching basenames, then asserts ChartChanges equals exactly the
// count ClassifyKind would call chart-kind — closing the whole class the
// case-insensitive LIKE predicate silently miscounted (e.g. "chart.yaml",
// "CHART.YAML" wrongly counted as chart changes).
func TestRepositoryStats_ChartChangesMatchesClassifyKindCaseSensitively(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	paths := []string{
		"Chart.yaml",           // exact match, root
		"chart.yaml",           // lowercase, root — NOT chart-kind
		"CHART.YAML",           // uppercase, root — NOT chart-kind
		"Chart.YAML",           // mixed case, root — NOT chart-kind
		"apps/foo/Chart.yaml",  // exact match, nested
		"apps/foo/chart.yaml",  // lowercase, nested — NOT chart-kind
		"apps/foo/CHART.YAML",  // uppercase, nested — NOT chart-kind
		"apps/foo/values.yaml", // unrelated basename — NOT chart-kind
		"apps/foo/xChart.yaml", // basename isn't exactly Chart.yaml — NOT chart-kind
		"apps/foo/Chart.yamlx", // basename isn't exactly Chart.yaml — NOT chart-kind
	}

	const repo = "case-repo"
	changes := make([]domain.Change, len(paths))
	wantChartChanges := 0
	for i, p := range paths {
		if changeset.ClassifyKind(p) == changeset.KindChart {
			wantChartChanges++
		}
		changes[i] = domain.Change{
			Repo:        repo,
			FilePath:    p,
			Field:       "version",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("1.0.0"),
			NewValue:    ptr("1.1.0"),
			CommitSha:   fmt.Sprintf("case-sha-%d", i),
			Author:      "case-tester",
			CommittedAt: repoStatsBase.Add(time.Duration(i) * time.Minute),
		}
	}

	seedChanges(t, s, changes)

	got, err := s.RepositoryStats()
	if err != nil {
		t.Fatalf("RepositoryStats: %v", err)
	}

	byRepo := make(map[string]store.RepositoryStats, len(got))
	for _, rs := range got {
		byRepo[rs.Repo] = rs
	}

	stats, ok := byRepo[repo]
	if !ok {
		t.Fatalf("missing %s in %#v", repo, got)
	}
	if stats.ChangeCount != len(paths) {
		t.Errorf("%s.ChangeCount = %d, want %d", repo, stats.ChangeCount, len(paths))
	}
	if stats.ChartChanges != wantChartChanges {
		t.Errorf("%s.ChartChanges = %d, want %d (ClassifyKind-derived) for paths %v", repo, stats.ChartChanges, wantChartChanges, paths)
	}
}
