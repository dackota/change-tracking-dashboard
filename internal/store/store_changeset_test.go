package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/filter"
)

// csBase is the reference commit time for changeset query tests.
var csBase = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

// TestQueryChangesets_GroupsChangesByCommit verifies that multiple Changes
// sharing a commit are returned as a single Changeset, and Changes from a
// different commit form a separate Changeset — the store's job is fetching
// the right rows and grouping/paginating, not re-deriving grouping logic.
func TestQueryChangesets_GroupsChangesByCommit(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	commitA1 := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
		Field:       "image-tag-a",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "commit-a",
		Author:      "alice",
		CommittedAt: csBase,
	}
	commitA2 := commitA1
	commitA2.Field = "image-tag-b"
	commitA2.OldValue = ptr("v3")
	commitA2.NewValue = ptr("v4")

	commitB1 := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-one/prod/eu-west-1/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v5"),
		NewValue:    ptr("v6"),
		Facets:      map[string]string{"env": "prod"},
		CommitSha:   "commit-b",
		Author:      "bob",
		CommittedAt: csBase.Add(time.Hour),
	}

	seedChanges(t, s, []domain.Change{commitA1, commitA2, commitB1})

	page, err := s.QueryChangesets(csBase.Add(2*time.Hour), filter.FilterSpec{}, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	if len(page.Changesets) != 2 {
		t.Fatalf("got %d Changesets, want 2 (one per commit)", len(page.Changesets))
	}

	// Most-recent-first: commit-b (newer) first, commit-a second.
	if page.Changesets[0].CommitSha != "commit-b" {
		t.Errorf("Changesets[0].CommitSha = %q, want commit-b", page.Changesets[0].CommitSha)
	}
	if page.Changesets[1].CommitSha != "commit-a" {
		t.Errorf("Changesets[1].CommitSha = %q, want commit-a", page.Changesets[1].CommitSha)
	}

	// commit-a's Changeset must carry both of its Changes.
	if len(page.Changesets[1].Changes) != 2 {
		t.Fatalf("commit-a Changeset has %d Changes, want 2", len(page.Changesets[1].Changes))
	}
}

// TestQueryChangesets_AsOfBoundIsStrictlyLessThan verifies the committedAt <
// asOf bound: a commit exactly at asOf is excluded (strictly-before, not
// on-or-before), while a commit before asOf is included.
func TestQueryChangesets_AsOfBoundIsStrictlyLessThan(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	before := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		CommitSha:   "commit-before",
		Author:      "alice",
		CommittedAt: csBase,
	}
	atBound := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v3"),
		NewValue:    ptr("v4"),
		CommitSha:   "commit-at-bound",
		Author:      "bob",
		CommittedAt: csBase.Add(time.Hour),
	}
	seedChanges(t, s, []domain.Change{before, atBound})

	page, err := s.QueryChangesets(csBase.Add(time.Hour), filter.FilterSpec{}, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	if len(page.Changesets) != 1 {
		t.Fatalf("got %d Changesets, want 1 (commit exactly at asOf must be excluded)", len(page.Changesets))
	}
	if page.Changesets[0].CommitSha != "commit-before" {
		t.Errorf("Changesets[0].CommitSha = %q, want commit-before", page.Changesets[0].CommitSha)
	}
}

// TestQueryChangesets_IncludeFilter verifies that an include filter in the
// FilterSpec is translated to SQL and returns only Changesets whose Changes
// carry a matching facet value.
func TestQueryChangesets_IncludeFilter(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	dev := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:    map[string]string{"env": "dev"},
		CommitSha: "commit-dev", Author: "alice", CommittedAt: csBase,
	}
	prod := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:    map[string]string{"env": "prod"},
		CommitSha: "commit-prod", Author: "bob", CommittedAt: csBase.Add(time.Hour),
	}
	seedChanges(t, s, []domain.Change{dev, prod})

	spec, err := filter.Parse(map[string][]string{"env": {"dev"}}, map[string]struct{}{"env": {}})
	if err != nil {
		t.Fatalf("filter.Parse: %v", err)
	}

	page, err := s.QueryChangesets(csBase.Add(2*time.Hour), spec, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	if len(page.Changesets) != 1 {
		t.Fatalf("got %d Changesets, want 1 (env=dev only)", len(page.Changesets))
	}
	if page.Changesets[0].CommitSha != "commit-dev" {
		t.Errorf("Changesets[0].CommitSha = %q, want commit-dev", page.Changesets[0].CommitSha)
	}
}

// TestQueryChangesets_ExcludeFilter_FacetAbsentSurvives is the critical
// exclude semantic from the PRD: excluding tier=sbx must fire only on an
// explicit facet match. A Change whose facets map has no "tier" key at all
// must stay visible — a naive SQL `json_extract(...) NOT IN (...)` clause
// would wrongly drop it, since json_extract returns NULL for an absent key
// and NULL NOT IN (...) evaluates to NULL (falsy), not true.
func TestQueryChangesets_ExcludeFilter_FacetAbsentSurvives(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	sbxChange := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"tier": "sbx"},
		CommitSha:   "commit-sbx",
		Author:      "alice",
		CommittedAt: csBase,
	}
	noTierChange := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev"}, // no "tier" facet at all
		CommitSha:   "commit-no-tier",
		Author:      "bob",
		CommittedAt: csBase.Add(time.Hour),
	}
	devTierChange := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"tier": "dev"}, // present but different value
		CommitSha:   "commit-dev-tier",
		Author:      "carol",
		CommittedAt: csBase.Add(2 * time.Hour),
	}
	seedChanges(t, s, []domain.Change{sbxChange, noTierChange, devTierChange})

	spec, err := filter.Parse(map[string][]string{"tier": {"-sbx"}}, map[string]struct{}{"tier": {}})
	if err != nil {
		t.Fatalf("filter.Parse: %v", err)
	}

	page, err := s.QueryChangesets(csBase.Add(3*time.Hour), spec, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	if len(page.Changesets) != 2 {
		t.Fatalf("got %d Changesets, want 2 (sbx excluded; facet-absent and different-value survive)", len(page.Changesets))
	}

	gotShas := map[string]bool{}
	for _, cs := range page.Changesets {
		gotShas[cs.CommitSha] = true
	}
	if gotShas["commit-sbx"] {
		t.Error("commit-sbx (explicit tier=sbx match) should have been excluded")
	}
	if !gotShas["commit-no-tier"] {
		t.Error("commit-no-tier (facet absent) should survive the exclude")
	}
	if !gotShas["commit-dev-tier"] {
		t.Error("commit-dev-tier (tier present but different value) should survive the exclude")
	}
}

// TestQueryChangesets_IncludeAndExcludeCombined verifies include-AND ∧
// exclude-none: a Changeset must satisfy the include filter AND not trigger
// any exclude filter.
func TestQueryChangesets_IncludeAndExcludeCombined(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	devSbx := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev", "tier": "sbx"},
		CommitSha:   "commit-dev-sbx",
		Author:      "alice",
		CommittedAt: csBase,
	}
	devNoTier := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "commit-dev-no-tier",
		Author:      "bob",
		CommittedAt: csBase.Add(time.Hour),
	}
	prodNoTier := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		Facets:      map[string]string{"env": "prod"},
		CommitSha:   "commit-prod-no-tier",
		Author:      "carol",
		CommittedAt: csBase.Add(2 * time.Hour),
	}
	seedChanges(t, s, []domain.Change{devSbx, devNoTier, prodNoTier})

	spec, err := filter.Parse(
		map[string][]string{"env": {"dev"}, "tier": {"-sbx"}},
		map[string]struct{}{"env": {}, "tier": {}},
	)
	if err != nil {
		t.Fatalf("filter.Parse: %v", err)
	}

	page, err := s.QueryChangesets(csBase.Add(3*time.Hour), spec, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	if len(page.Changesets) != 1 {
		t.Fatalf("got %d Changesets, want 1 (env=dev AND NOT tier=sbx)", len(page.Changesets))
	}
	if page.Changesets[0].CommitSha != "commit-dev-no-tier" {
		t.Errorf("Changesets[0].CommitSha = %q, want commit-dev-no-tier", page.Changesets[0].CommitSha)
	}
}

// TestQueryChangesets_MostRecentFirstOrdering verifies that Changesets are
// ordered newest-commit-first, regardless of insertion order — the likeliest
// incident culprits surface at the top of the page.
func TestQueryChangesets_MostRecentFirstOrdering(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	oldest := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-oldest", Author: "alice", CommittedAt: csBase,
	}
	newest := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-newest", Author: "bob", CommittedAt: csBase.Add(2 * time.Hour),
	}
	middle := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-middle", Author: "carol", CommittedAt: csBase.Add(time.Hour),
	}
	// Insert deliberately out of chronological order.
	seedChanges(t, s, []domain.Change{oldest, newest, middle})

	page, err := s.QueryChangesets(csBase.Add(3*time.Hour), filter.FilterSpec{}, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	wantOrder := []string{"commit-newest", "commit-middle", "commit-oldest"}
	if len(page.Changesets) != len(wantOrder) {
		t.Fatalf("got %d Changesets, want %d", len(page.Changesets), len(wantOrder))
	}
	for i, want := range wantOrder {
		if page.Changesets[i].CommitSha != want {
			t.Errorf("Changesets[%d].CommitSha = %q, want %q", i, page.Changesets[i].CommitSha, want)
		}
	}
}

// TestQueryChangesets_Pagination_WalksFullSetWithNoGapsOrOverlaps verifies
// that following NextCursor page after page returns every Changeset exactly
// once, in the same most-recent-first order as a single unbounded query —
// pagination must be effectively unbounded with no gaps or overlaps.
func TestQueryChangesets_Pagination_WalksFullSetWithNoGapsOrOverlaps(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	const totalCommits = 7
	var all []domain.Change
	for i := 0; i < totalCommits; i++ {
		all = append(all, domain.Change{
			Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("a"),
			NewValue:    ptr("b"),
			CommitSha:   fmt.Sprintf("commit-%02d", i),
			Author:      "alice",
			CommittedAt: csBase.Add(time.Duration(i) * time.Hour),
		})
	}
	seedChanges(t, s, all)

	asOf := csBase.Add(time.Duration(totalCommits) * time.Hour)
	const pageSize = 3

	var gotShas []string
	cursor := ""
	for pages := 0; pages < totalCommits+1; pages++ { // hard cap to avoid an infinite loop on a bug
		page, err := s.QueryChangesets(asOf, filter.FilterSpec{}, cursor, pageSize)
		if err != nil {
			t.Fatalf("QueryChangesets (cursor=%q): %v", cursor, err)
		}
		for _, cs := range page.Changesets {
			gotShas = append(gotShas, cs.CommitSha)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if len(gotShas) != totalCommits {
		t.Fatalf("walked %d Changesets across all pages, want %d (no gaps/overlaps): %v", len(gotShas), totalCommits, gotShas)
	}

	// Expected order: newest-first, i.e. commit-06 down to commit-00.
	wantShas := make([]string, totalCommits)
	for i := 0; i < totalCommits; i++ {
		wantShas[i] = fmt.Sprintf("commit-%02d", totalCommits-1-i)
	}
	for i, want := range wantShas {
		if gotShas[i] != want {
			t.Errorf("gotShas[%d] = %q, want %q (full order: %v)", i, gotShas[i], want, gotShas)
			break
		}
	}
}

// TestQueryChangesets_Pagination_LastPageHasEmptyNextCursor verifies that
// once the final page is reached, NextCursor is empty — signalling the
// caller there is nothing further to fetch.
func TestQueryChangesets_Pagination_LastPageHasEmptyNextCursor(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	c := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-only", Author: "alice", CommittedAt: csBase,
	}
	seedChanges(t, s, []domain.Change{c})

	page, err := s.QueryChangesets(csBase.Add(time.Hour), filter.FilterSpec{}, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	if len(page.Changesets) != 1 {
		t.Fatalf("got %d Changesets, want 1", len(page.Changesets))
	}
	if page.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty (last/only page)", page.NextCursor)
	}
}

// TestQueryChangesets_MixedCommitYieldsOneChangesetWithDifferingKinds
// verifies that a single commit touching both a Chart.yaml and a
// values.yaml file is returned as one Changeset whose Changes carry
// different Kinds — the store's grouping must not split a commit just
// because it produced changes classified differently.
func TestQueryChangesets_MixedCommitYieldsOneChangesetWithDifferingKinds(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	chartChange := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.0.0"),
		NewValue:    ptr("1.1.0"),
		CommitSha:   "mixed-commit",
		Author:      "dana",
		CommittedAt: csBase,
	}
	valueChange := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		CommitSha:   "mixed-commit",
		Author:      "dana",
		CommittedAt: csBase,
	}
	seedChanges(t, s, []domain.Change{chartChange, valueChange})

	page, err := s.QueryChangesets(csBase.Add(time.Hour), filter.FilterSpec{}, "", 100)
	if err != nil {
		t.Fatalf("QueryChangesets: %v", err)
	}

	if len(page.Changesets) != 1 {
		t.Fatalf("got %d Changesets, want 1 (mixed commit must not split)", len(page.Changesets))
	}

	cs := page.Changesets[0]
	if len(cs.Changes) != 2 {
		t.Fatalf("len(Changes) = %d, want 2", len(cs.Changes))
	}

	gotKinds := map[changeset.Kind]int{}
	for _, c := range cs.Changes {
		gotKinds[c.Kind]++
	}
	if gotKinds[changeset.KindChart] != 1 {
		t.Errorf("KindChart count = %d, want 1", gotKinds[changeset.KindChart])
	}
	if gotKinds[changeset.KindValue] != 1 {
		t.Errorf("KindValue count = %d, want 1", gotKinds[changeset.KindValue])
	}
}

// TestQueryChangesets_RejectsUnsafeFacetKey verifies the store boundary
// guard still applies to Changeset queries: a facet key that is not a safe
// identifier is rejected rather than concatenated into the json_extract
// path, whether it arrives via an include or an exclude. filter.Parse's
// caller-supplied allowlist means an unsafe key can, in principle, still
// reach a FilterSpec, so the store must not trust it.
func TestQueryChangesets_RejectsUnsafeFacetKey(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	const unsafeKey = "env') = '' OR '1'='1"

	includeSpec, err := filter.Parse(
		map[string][]string{unsafeKey: {"x"}},
		map[string]struct{}{unsafeKey: {}},
	)
	if err != nil {
		t.Fatalf("filter.Parse (include): %v", err)
	}
	if _, err := s.QueryChangesets(csBase.Add(time.Hour), includeSpec, "", 100); err == nil {
		t.Error("QueryChangesets: expected an error for an unsafe include facet key, got nil")
	}

	excludeSpec, err := filter.Parse(
		map[string][]string{unsafeKey: {"-x"}},
		map[string]struct{}{unsafeKey: {}},
	)
	if err != nil {
		t.Fatalf("filter.Parse (exclude): %v", err)
	}
	if _, err := s.QueryChangesets(csBase.Add(time.Hour), excludeSpec, "", 100); err == nil {
		t.Error("QueryChangesets: expected an error for an unsafe exclude facet key, got nil")
	}
}
