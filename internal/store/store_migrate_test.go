package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // register the "sqlite" driver for the legacy-DB fixture

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

// legacyHWMSchema is the high_water_marks table as it existed BEFORE the
// per-field key: keyed by (repo, file_path) with no `field` column. A
// production database created by an earlier release looks exactly like this.
const legacyHWMSchema = `
CREATE TABLE high_water_marks (
    repo      TEXT NOT NULL,
    file_path TEXT NOT NULL,
    sha       TEXT NOT NULL,
    PRIMARY KEY (repo, file_path)
);`

// legacyChangesSchema is the changes table as it existed BEFORE the 0.9.0
// issue-correlation feature (PR #77) added the issue_refs_json column. A
// production database created by an earlier release looks exactly like this.
const legacyChangesSchema = `
CREATE TABLE changes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo          TEXT    NOT NULL,
    file_path     TEXT    NOT NULL,
    field         TEXT    NOT NULL,
    key_val       TEXT,
    change_type   TEXT    NOT NULL,
    old_value     TEXT,
    new_value     TEXT,
    facets_json   TEXT    NOT NULL DEFAULT '{}',
    commit_sha    TEXT    NOT NULL,
    author        TEXT    NOT NULL,
    committed_at  TEXT    NOT NULL
);`

// TestOpen_MigratesLegacyDBMissingIssueRefsColumn reproduces the production
// outage: a pre-0.9.0 database whose changes table lacks issue_refs_json. The
// schema is created with CREATE TABLE IF NOT EXISTS, so re-running it against an
// existing table is a no-op that never adds the new column — every changeset
// query then fails with "no such column: c.issue_refs_json". Opening the store
// must migrate the column in and let the previously-failing query succeed,
// reading the legacy row back with the empty-slice default for IssueRefs.
func TestOpen_MigratesLegacyDBMissingIssueRefsColumn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// Arrange: build a legacy database directly, bypassing the store.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(legacyChangesSchema); err != nil {
		t.Fatalf("create legacy changes table: %v", err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO changes (repo, file_path, field, change_type, commit_sha, author, committed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"apps-repo", "versions.tf", "google-provider-version", "modified",
		"sha-legacy", "alice", "2024-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Act: open the store, which must run the additive migration on boot.
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open on legacy db: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Assert: the query that failed in production now succeeds, and the legacy
	// row reads back with the empty-slice default for IssueRefs.
	feed, err := s.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed after migrating legacy db: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("QueryFeed returned %d rows, want 1", len(feed))
	}
	if got := feed[0].IssueRefs; len(got) != 0 {
		t.Errorf("legacy row IssueRefs = %#v, want empty", got)
	}

	// Also exercise the exact call that failed in production: the "kpi
	// changesets" path, whose SQL selects the aliased c.issue_refs_json.
	asOf := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.QueryChangesets(asOf, filter.FilterSpec{}, "", 100); err != nil {
		t.Fatalf("QueryChangesets after migrating legacy db: %v", err)
	}
}

// TestOpen_MigrationIsIdempotent guarantees the additive migration is safe to
// run on every boot: opening a database that already has issue_refs_json (the
// fresh-DB and already-migrated cases) must not error or corrupt data.
func TestOpen_MigrationIsIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "repeat.db")

	// First open creates the schema (column present from the start).
	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open (first): %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close (first): %v", err)
	}

	// Second open re-runs migrate() against the already-migrated DB.
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open (second, already migrated): %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	if _, err := s2.QueryFeed(100); err != nil {
		t.Fatalf("QueryFeed after reopening migrated db: %v", err)
	}
}

// TestOpen_MigratesLegacyHWMTableToPerField reproduces a pre-per-field volume:
// a high_water_marks table keyed by (repo, file_path) with no `field` column.
// SQLite cannot ALTER a PRIMARY KEY, so opening the store must rebuild the
// table with the per-field key — after which two fields on the SAME file resume
// independently, which the old key made impossible (the second write would
// collide on / overwrite the first).
func TestOpen_MigratesLegacyHWMTableToPerField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy-hwm.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(legacyHWMSchema); err != nil {
		t.Fatalf("create legacy high_water_marks table: %v", err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO high_water_marks (repo, file_path, sha) VALUES (?, ?, ?)`,
		"infra-repo", "terraform/versions.tf", "sha-legacy",
	); err != nil {
		t.Fatalf("insert legacy hwm row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open on legacy hwm db: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	const repo = "infra-repo"
	const file = "terraform/versions.tf"
	if err := s.SetHighWaterMark(repo, file, "kubernetes-version", "sha-k8s"); err != nil {
		t.Fatalf("SetHighWaterMark (field A): %v", err)
	}
	if err := s.SetHighWaterMark(repo, file, "oci-provider-version", "sha-oci"); err != nil {
		t.Fatalf("SetHighWaterMark (field B): %v", err)
	}

	gotA, err := s.GetHighWaterMark(repo, file, "kubernetes-version")
	if err != nil {
		t.Fatalf("GetHighWaterMark (field A): %v", err)
	}
	gotB, err := s.GetHighWaterMark(repo, file, "oci-provider-version")
	if err != nil {
		t.Fatalf("GetHighWaterMark (field B): %v", err)
	}
	if gotA != "sha-k8s" || gotB != "sha-oci" {
		t.Errorf("per-field HWM after migration: A=%q B=%q, want sha-k8s / sha-oci (independent per-field cursors)", gotA, gotB)
	}
}

// TestOpen_DedupesLegacyDuplicateChanges reproduces a database written before
// SaveChange was idempotent: a changes table with duplicate rows sharing one
// identity (repo, file_path, field, key, commit_sha). Opening the store must
// collapse them to one and enforce uniqueness thereafter, so a later re-walk of
// history cannot reintroduce the duplicate.
func TestOpen_DedupesLegacyDuplicateChanges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "dup-changes.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(legacyChangesSchema); err != nil {
		t.Fatalf("create legacy changes table: %v", err)
	}
	const insert = `INSERT INTO changes (repo, file_path, field, change_type, commit_sha, author, committed_at)
	                VALUES (?, ?, ?, ?, ?, ?, ?)`
	for i := 0; i < 2; i++ { // two identical-identity rows (nil scalar key)
		if _, err := legacy.Exec(insert,
			"infra-repo", "terraform/cluster.tf", "kubernetes-version", "modified",
			"sha-dup", "dev", "2024-01-01T00:00:00Z",
		); err != nil {
			t.Fatalf("insert duplicate row %d: %v", i, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open on db with duplicate changes: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	feed, err := s.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed after dedupe migration: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("after dedupe migration: got %d rows, want 1", len(feed))
	}

	// Uniqueness is now enforced: re-saving the same identity is a no-op.
	if err := s.SaveChange(domain.Change{
		Repo: "infra-repo", FilePath: "terraform/cluster.tf", Field: "kubernetes-version",
		ChangeType: domain.ChangeTypeModified, CommitSha: "sha-dup", Author: "dev",
		CommittedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("SaveChange (idempotent re-save): %v", err)
	}
	feed, err = s.QueryFeed(100)
	if err != nil {
		t.Fatalf("QueryFeed after idempotent re-save: %v", err)
	}
	if len(feed) != 1 {
		t.Fatalf("after idempotent re-save: got %d rows, want 1", len(feed))
	}
}
