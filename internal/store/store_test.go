package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
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

	// Reading before any write returns empty string.
	sha, err := s.GetHighWaterMark("apps-repo", filePath)
	if err != nil {
		t.Fatalf("GetHighWaterMark (empty): %v", err)
	}
	if sha != "" {
		t.Errorf("GetHighWaterMark (empty): got %q, want empty string", sha)
	}

	// Write a mark.
	if err := s.SetHighWaterMark("apps-repo", filePath, "abc123"); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	// Read it back.
	sha, err = s.GetHighWaterMark("apps-repo", filePath)
	if err != nil {
		t.Fatalf("GetHighWaterMark (after set): %v", err)
	}
	if sha != "abc123" {
		t.Errorf("GetHighWaterMark: got %q, want abc123", sha)
	}

	// Overwrite the mark.
	if err := s.SetHighWaterMark("apps-repo", filePath, "def456"); err != nil {
		t.Fatalf("SetHighWaterMark (overwrite): %v", err)
	}
	sha, err = s.GetHighWaterMark("apps-repo", filePath)
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

	if err := s.SetHighWaterMark(repo, fileA, "sha-a-1"); err != nil {
		t.Fatalf("SetHighWaterMark (fileA): %v", err)
	}
	if err := s.SetHighWaterMark(repo, fileB, "sha-b-1"); err != nil {
		t.Fatalf("SetHighWaterMark (fileB): %v", err)
	}

	gotA, err := s.GetHighWaterMark(repo, fileA)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileA): %v", err)
	}
	if gotA != "sha-a-1" {
		t.Errorf("GetHighWaterMark(fileA) = %q, want sha-a-1 (must not be clobbered by fileB)", gotA)
	}

	gotB, err := s.GetHighWaterMark(repo, fileB)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileB): %v", err)
	}
	if gotB != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fileB) = %q, want sha-b-1", gotB)
	}

	// Advancing fileA's mark must not affect fileB's.
	if err := s.SetHighWaterMark(repo, fileA, "sha-a-2"); err != nil {
		t.Fatalf("SetHighWaterMark (fileA advance): %v", err)
	}
	gotB2, err := s.GetHighWaterMark(repo, fileB)
	if err != nil {
		t.Fatalf("GetHighWaterMark (fileB after fileA advance): %v", err)
	}
	if gotB2 != "sha-b-1" {
		t.Errorf("GetHighWaterMark(fileB) after advancing fileA = %q, want unchanged sha-b-1", gotB2)
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
