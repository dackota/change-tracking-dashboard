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

	// Reading before any write returns empty string.
	sha, err := s.GetHighWaterMark("apps-repo")
	if err != nil {
		t.Fatalf("GetHighWaterMark (empty): %v", err)
	}
	if sha != "" {
		t.Errorf("GetHighWaterMark (empty): got %q, want empty string", sha)
	}

	// Write a mark.
	if err := s.SetHighWaterMark("apps-repo", "abc123"); err != nil {
		t.Fatalf("SetHighWaterMark: %v", err)
	}

	// Read it back.
	sha, err = s.GetHighWaterMark("apps-repo")
	if err != nil {
		t.Fatalf("GetHighWaterMark (after set): %v", err)
	}
	if sha != "abc123" {
		t.Errorf("GetHighWaterMark: got %q, want abc123", sha)
	}

	// Overwrite the mark.
	if err := s.SetHighWaterMark("apps-repo", "def456"); err != nil {
		t.Fatalf("SetHighWaterMark (overwrite): %v", err)
	}
	sha, err = s.GetHighWaterMark("apps-repo")
	if err != nil {
		t.Fatalf("GetHighWaterMark (overwrite): %v", err)
	}
	if sha != "def456" {
		t.Errorf("GetHighWaterMark (overwrite): got %q, want def456", sha)
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
