package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

func ptr(s string) *string { return &s }

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPersistAndQueryFeed(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	changes := []domain.Change{
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			Field:       "aidp-version",
			Key:         nil,
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("1.0.0"),
			NewValue:    ptr("1.1.0"),
			Facets:      map[string]string{"tenant": "tenant-zero", "env": "dev", "region": "us-west-2"},
			CommitSha:   "sha-001",
			Author:      "alice",
			CommittedAt: base,
		},
		{
			Repo:        "apps-repo",
			FilePath:    "apps/tenant-one/prod/eu-west-1/Chart.yaml",
			Field:       "aidp-version",
			Key:         nil,
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("2.0.0"),
			NewValue:    ptr("2.1.0"),
			Facets:      map[string]string{"tenant": "tenant-one", "env": "prod", "region": "eu-west-1"},
			CommitSha:   "sha-002",
			Author:      "bob",
			CommittedAt: base.Add(time.Hour),
		},
	}

	for _, c := range changes {
		if err := s.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}

	feed, err := s.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	if len(feed) != 2 {
		t.Fatalf("QueryFeed returned %d changes, want 2", len(feed))
	}

	// Newest first: sha-002 (base+1h) should be first.
	if feed[0].CommitSha != "sha-002" {
		t.Errorf("feed[0].CommitSha = %q, want sha-002 (newest first)", feed[0].CommitSha)
	}
	if feed[1].CommitSha != "sha-001" {
		t.Errorf("feed[1].CommitSha = %q, want sha-001", feed[1].CommitSha)
	}

	// Verify round-trip of facets.
	if feed[0].Facets["tenant"] != "tenant-one" {
		t.Errorf("facet tenant: got %q, want tenant-one", feed[0].Facets["tenant"])
	}
	if feed[1].Facets["env"] != "dev" {
		t.Errorf("facet env: got %q, want dev", feed[1].Facets["env"])
	}
}

func TestHighWaterMark(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	const filePath = "apps/tenant-zero/dev/us-west-2/Chart.yaml"
	const field = "chart-version"

	// Reading before any write returns empty string.
	sha, err := s.GetHighWaterMark("apps-repo", filePath, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (empty): %v", err)
	}
	if sha != "" {
		t.Errorf("GetHighWaterMark (empty): got %q, want empty string", sha)
	}

	// Write a mark.
	if err := s.SetHighWaterMark("apps-repo", filePath, field, "abc123"); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	// Read it back.
	sha, err = s.GetHighWaterMark("apps-repo", filePath, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (after set): %v", err)
	}
	if sha != "abc123" {
		t.Errorf("GetHighWaterMark: got %q, want abc123", sha)
	}

	// Overwrite the mark.
	if err := s.SetHighWaterMark("apps-repo", filePath, field, "def456"); err != nil {
		t.Fatalf("SetHighWaterMark (overwrite): %v", err)
	}
	sha, err = s.GetHighWaterMark("apps-repo", filePath, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (overwrite): %v", err)
	}
	if sha != "def456" {
		t.Errorf("GetHighWaterMark (overwrite): got %q, want def456", sha)
	}
}

// TestHighWaterMark_PerFileGranularity verifies that the HWM is keyed at
// (repo, filePath) granularity — two different files in the same repo each
// resume independently and do not clobber each other's mark. This is the
// critical correctness property that glob fan-out depends on: walking many
// matched files through the same repo must not share one cursor.
func TestHighWaterMark_PerFileGranularity(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	const repo = "apps-repo"
	const fileA = "a/x/Chart.yaml"
	const fileB = "a/y/Chart.yaml"
	const field = "chart-version"

	if err := s.SetHighWaterMark(repo, fileA, field, "sha-a-1"); err != nil {
		t.Fatalf("SetHighWaterMark (fileA): %v", err)
	}
	if err := s.SetHighWaterMark(repo, fileB, field, "sha-b-1"); err != nil {
		t.Fatalf("SetHighWaterMark (fileB): %v", err)
	}

	gotA, err := s.GetHighWaterMark(repo, fileA, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileA): %v", err)
	}
	if gotA != "sha-a-1" {
		t.Errorf("GetHighWaterMark(fileA) = %q, want sha-a-1 (must not be clobbered by fileB)", gotA)
	}

	gotB, err := s.GetHighWaterMark(repo, fileB, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileB): %v", err)
	}
	if gotB != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fileB) = %q, want sha-b-1", gotB)
	}

	// Advancing fileA's mark must not affect fileB's.
	if err := s.SetHighWaterMark(repo, fileA, field, "sha-a-2"); err != nil {
		t.Fatalf("SetHighWaterMark (fileA advance): %v", err)
	}
	gotB2, err := s.GetHighWaterMark(repo, fileB, field)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileB after fileA advance): %v", err)
	}
	if gotB2 != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fileB) after advancing fileA = %q, want unchanged sha-b-1", gotB2)
	}
}

// TestHighWaterMark_PerFieldGranularity verifies that the HWM is keyed by
// (repo, filePath, field): two fields tracked from the SAME file each resume
// from their own cursor and never clobber each other. This is the property the
// per-file-shared-cursor bug violated — the first field polled advanced the
// shared mark, silently starving every other field's backfill.
func TestHighWaterMark_PerFieldGranularity(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	const repo = "infra-repo"
	const file = "terraform/versions.tf"
	const fieldA = "kubernetes-version"
	const fieldB = "oci-provider-version"

	if err := s.SetHighWaterMark(repo, file, fieldA, "sha-a-1"); err != nil {
		t.Fatalf("SetHighWaterMark (fieldA): %v", err)
	}
	if err := s.SetHighWaterMark(repo, file, fieldB, "sha-b-1"); err != nil {
		t.Fatalf("SetHighWaterMark (fieldB): %v", err)
	}

	gotA, err := s.GetHighWaterMark(repo, file, fieldA)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fieldA): %v", err)
	}
	if gotA != "sha-a-1" {
		t.Errorf("GetHighWaterMark(fieldA) = %q, want sha-a-1 (must not be clobbered by fieldB on the same file)", gotA)
	}

	gotB, err := s.GetHighWaterMark(repo, file, fieldB)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fieldB): %v", err)
	}
	if gotB != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fieldB) = %q, want sha-b-1", gotB)
	}

	// Advancing fieldA's mark on the shared file must not touch fieldB's.
	if err := s.SetHighWaterMark(repo, file, fieldA, "sha-a-2"); err != nil {
		t.Fatalf("SetHighWaterMark (fieldA advance): %v", err)
	}
	gotB2, err := s.GetHighWaterMark(repo, file, fieldB)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fieldB after fieldA advance): %v", err)
	}
	if gotB2 != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fieldB) after advancing fieldA on the same file = %q, want unchanged sha-b-1", gotB2)
	}
}

// TestSaveChange_Idempotent verifies that saving the same change twice — same
// (repo, file_path, field, key, commit_sha) identity — records it once. This is
// what lets a one-time cursor rebuild re-walk history without duplicating rows
// already in the feed. Covers both a scalar-key (nil) change and a keyed one,
// since SQLite's NULL-distinct rule would otherwise let nil-key rows duplicate.
func TestSaveChange_Idempotent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	key := "argo-cd"
	scalar := domain.Change{
		Repo: "infra-repo", FilePath: "terraform/oci-containerengine-cluster.tf",
		Field: "kubernetes-version", Key: nil, ChangeType: domain.ChangeTypeModified,
		OldValue: ptr("v1.35.1"), NewValue: ptr("v1.36.1"),
		CommitSha: "sha-k8s", Author: "dev", CommittedAt: base,
	}
	keyed := domain.Change{
		Repo: "apps-repo", FilePath: "gitops/platform/argocd/Chart.yaml",
		Field: "chartDependencies", Key: &key, ChangeType: domain.ChangeTypeModified,
		OldValue: ptr("10.1.2"), NewValue: ptr("10.1.3"),
		CommitSha: "sha-argo", Author: "dev", CommittedAt: base.Add(time.Hour),
	}

	for _, c := range []domain.Change{scalar, keyed} {
		// Save each change twice — the second write must be a no-op.
		if err := s.SaveChange(c); err != nil {
			t.Fatalf("SaveChange (first): %v", err)
		}
		if err := s.SaveChange(c); err != nil {
			t.Fatalf("SaveChange (duplicate): %v", err)
		}
	}

	feed, err := s.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 2 {
		t.Fatalf("QueryFeed returned %d changes, want 2 (each saved-twice change recorded once)", len(feed))
	}
}

func TestQueryFeedEmptyDatabase(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	feed, err := s.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed (empty): %v", err)
	}
	if len(feed) != 0 {
		t.Errorf("QueryFeed (empty): got %d changes, want 0", len(feed))
	}
}

// TestKeyedChangeRoundTrip confirms that a Change with a non-nil Key persists
// and reads back with its Key intact through SaveChange → QueryFeed.
func TestKeyedChangeRoundTrip(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	key := "aidp-gateway"
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "aidp/k8/Chart.yaml",
		Field:       "subchart-versions",
		Key:         &key,
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("0.38.0"),
		NewValue:    ptr("0.39.0"),
		Facets:      map[string]string{"env": "dev"},
		CommitSha:   "sha-keyed-001",
		Author:      "alice",
		CommittedAt: time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
	}

	if err := s.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	feed, err := s.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}

	if len(feed) != 1 {
		t.Fatalf("QueryFeed returned %d changes, want 1", len(feed))
	}

	got := feed[0]

	// Key must round-trip.
	if got.Key == nil {
		t.Fatal("Key is nil after round-trip, want non-nil")
	}
	if *got.Key != key {
		t.Errorf("Key = %q, want %q", *got.Key, key)
	}

	// Other fields must also be intact.
	if got.ChangeType != domain.ChangeTypeModified {
		t.Errorf("ChangeType = %q, want modified", got.ChangeType)
	}
	if got.OldValue == nil || *got.OldValue != "0.38.0" {
		t.Errorf("OldValue = %v, want 0.38.0", got.OldValue)
	}
	if got.NewValue == nil || *got.NewValue != "0.39.0" {
		t.Errorf("NewValue = %v, want 0.39.0", got.NewValue)
	}
	if got.Field != "subchart-versions" {
		t.Errorf("Field = %q, want subchart-versions", got.Field)
	}
}

// TestIssueRefsRoundTrip confirms that a Change's IssueRefs (issue/PR
// references parsed from its triggering commit message — see
// internal/issueref) persists and reads back intact through SaveChange ->
// QueryFeed, and that a Change with no references round-trips to an empty
// slice — never a false/spurious reference (mirrors the Facets and Key
// round-trip contracts already proven above).
func TestIssueRefsRoundTrip(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	withRefs := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.0.0"),
		NewValue:    ptr("5.10.0"),
		CommitSha:   "sha-with-refs",
		Author:      "alice",
		CommittedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		IssueRefs:   []string{"#123", "ABC-456"},
	}
	withoutRefs := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.10.0"),
		NewValue:    ptr("5.11.0"),
		CommitSha:   "sha-without-refs",
		Author:      "bob",
		CommittedAt: time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC),
		IssueRefs:   nil,
	}

	if err := s.SaveChange(withRefs); err != nil {
		t.Fatalf("SaveChange (withRefs): %v", err)
	}
	if err := s.SaveChange(withoutRefs); err != nil {
		t.Fatalf("SaveChange (withoutRefs): %v", err)
	}

	feed, err := s.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed: %v", err)
	}
	if len(feed) != 2 {
		t.Fatalf("QueryFeed returned %d changes, want 2", len(feed))
	}

	// Newest first: sha-without-refs (later CommittedAt) is feed[0].
	if got := feed[0].IssueRefs; len(got) != 0 {
		t.Errorf("feed[0] (sha-without-refs) IssueRefs = %#v, want empty (no false reference)", got)
	}
	if got := feed[1].IssueRefs; len(got) != 2 || got[0] != "#123" || got[1] != "ABC-456" {
		t.Errorf("feed[1] (sha-with-refs) IssueRefs = %#v, want [\"#123\", \"ABC-456\"]", got)
	}
}
