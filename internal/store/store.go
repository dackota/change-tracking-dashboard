// Package store implements the SQLite-backed repository for Changes and
// per-repo high-water-mark commit SHAs. It uses the pure-Go modernc.org/sqlite
// driver so no cgo or external sqlite binary is required.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	_ "modernc.org/sqlite" // register "sqlite" driver
)

const driverName = "sqlite"

// Store is the SQLite-backed repository. Call Close when done.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at the given file path and runs
// schema migrations. It returns a ready-to-use Store.
func Open(path string) (*Store, error) {
	db, err := sql.Open(driverName, path)
	if err != nil {
		return nil, fmt.Errorf("store: open db %q: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

const schemaChanges = `
CREATE TABLE IF NOT EXISTS changes (
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
    committed_at  TEXT    NOT NULL,
    issue_refs_json TEXT  NOT NULL DEFAULT '[]'
);`

// high_water_marks is keyed by (repo, file_path, field), not repo or
// (repo, file_path): a glob tracker fans out across many files, AND several
// trackers can extract different fields from the same file. Each
// (file, field) must resume from its own cursor. Keying only by file path let
// whichever field polled a file first advance the shared cursor to HEAD, so
// every other field on that file saw a non-empty cursor, skipped its first-run
// backfill, and silently never recorded its history — see the per-field
// backfill regression test.
const schemaHWM = `
CREATE TABLE IF NOT EXISTS high_water_marks (
    repo      TEXT NOT NULL,
    file_path TEXT NOT NULL,
    field     TEXT NOT NULL,
    sha       TEXT NOT NULL,
    PRIMARY KEY (repo, file_path, field)
);`

// indexChangesIdentity makes SaveChange idempotent: a change is uniquely
// identified by where it happened (repo, file_path, field, key) and when
// (commit_sha). COALESCE folds the nullable scalar key to an empty string so
// scalar-field rows dedupe too — SQLite treats every NULL as distinct in a
// UNIQUE index, so
// a bare key_val column would never collide for scalar fields. Idempotency lets
// a one-time cursor rebuild (see migrate) re-walk history without duplicating
// rows that were already recorded.
const indexChangesIdentity = `
CREATE UNIQUE INDEX IF NOT EXISTS ux_changes_identity
ON changes (repo, file_path, field, COALESCE(key_val, ''), commit_sha);`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaChanges); err != nil {
		return fmt.Errorf("create changes table: %w", err)
	}
	// A pre-per-field database has a high_water_marks table keyed by
	// (repo, file_path) with no `field` column. SQLite cannot ALTER a table's
	// PRIMARY KEY, so drop the stale table; schemaHWM below recreates it with
	// the per-field key. Cursors are discarded on purpose — the old key
	// clobbered per-field cursors, so a one-time re-backfill is exactly what
	// heals the fields it silently dropped. The re-backfill is duplicate-safe
	// because ux_changes_identity + INSERT OR IGNORE make SaveChange idempotent.
	if err := s.dropLegacyHighWaterMarks(); err != nil {
		return fmt.Errorf("drop legacy high_water_marks table: %w", err)
	}
	if _, err := s.db.Exec(schemaHWM); err != nil {
		return fmt.Errorf("create high_water_marks table: %w", err)
	}
	// Additive column migrations for databases created before a column
	// existed. CREATE TABLE IF NOT EXISTS never alters an already-present
	// table, so a pre-0.9.0 volume is missing issue_refs_json and every
	// changeset query fails with "no such column" until we add it here.
	if err := s.ensureColumn("changes", "issue_refs_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return fmt.Errorf("add changes.issue_refs_json column: %w", err)
	}
	// Collapse any duplicate change rows a prior (non-idempotent) re-backfill
	// may have left, keeping the earliest id per identity, before enforcing the
	// uniqueness the idempotent write path now relies on.
	if err := s.dedupeChanges(); err != nil {
		return fmt.Errorf("dedupe changes: %w", err)
	}
	if _, err := s.db.Exec(indexChangesIdentity); err != nil {
		return fmt.Errorf("create changes identity index: %w", err)
	}
	return nil
}

// dropLegacyHighWaterMarks drops a pre-per-field high_water_marks table (one
// with no `field` column) so migrate can recreate it with the per-field key. It
// is a no-op when the table is absent (fresh DB) or already per-field, so it is
// safe to run on every boot. The store assumes a single writer per database
// file (a ReadWriteOnce volume on one pod), so the inspect-then-drop is not
// synchronized.
func (s *Store) dropLegacyHighWaterMarks() error {
	rows, err := s.db.Query("PRAGMA table_info(high_water_marks)")
	if err != nil {
		return fmt.Errorf("inspect high_water_marks: %w", err)
	}
	defer rows.Close()
	var exists, hasField bool
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan high_water_marks columns: %w", err)
		}
		exists = true
		if name == "field" {
			hasField = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate high_water_marks columns: %w", err)
	}
	if exists && !hasField {
		if _, err := s.db.Exec("DROP TABLE high_water_marks"); err != nil {
			return fmt.Errorf("drop high_water_marks: %w", err)
		}
	}
	return nil
}

// dedupeChanges removes duplicate change rows that share an identity
// (repo, file_path, field, key, commit_sha), keeping the earliest-inserted
// (min id). It runs before ux_changes_identity is created so the unique index
// can be built even on a database that accumulated duplicates before the
// idempotent write path existed. On a clean database it deletes nothing.
func (s *Store) dedupeChanges() error {
	const query = `
DELETE FROM changes
WHERE id NOT IN (
    SELECT MIN(id) FROM changes
    GROUP BY repo, file_path, field, COALESCE(key_val, ''), commit_sha
)`
	if _, err := s.db.Exec(query); err != nil {
		return fmt.Errorf("delete duplicate changes: %w", err)
	}
	return nil
}

// ensureColumn adds column to table when it is not already present, and is a
// no-op when it is. It lets the schema evolve on an existing database without a
// full migration framework: a fresh DB gets the column from schemaChanges, an
// older one gets it via ALTER TABLE on the next boot. table and column are
// trusted internal identifiers (never user input), which is required because
// SQLite cannot parameterize identifiers in PRAGMA/ALTER statements.
//
// The store assumes a single writer per database file (the deployment backs it
// with a ReadWriteOnce volume attached to one pod). The check-then-ALTER below
// is therefore not synchronized; as defense-in-depth against a transient
// double-open during a rolling restart, a losing racer's "duplicate column"
// error is treated as success — the column exists, which is all we require.
func (s *Store) ensureColumn(table, column, decl string) error {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan %s columns: %w", table, err)
		}
		if name == column {
			return nil // already present — nothing to do
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s columns: %w", table, err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)); err != nil {
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil // a concurrent opener already added it
		}
		return fmt.Errorf("alter %s add %s: %w", table, column, err)
	}
	return nil
}

// SaveChange persists a single Change to the database.
func (s *Store) SaveChange(c domain.Change) error {
	facetsJSON, err := json.Marshal(c.Facets)
	if err != nil {
		return fmt.Errorf("store: marshal facets: %w", err)
	}
	issueRefsJSON, err := json.Marshal(c.IssueRefs)
	if err != nil {
		return fmt.Errorf("store: marshal issue refs: %w", err)
	}

	// OR IGNORE makes the write idempotent against ux_changes_identity: a change
	// already recorded (same repo/file/field/key/commit) is silently skipped, so
	// re-walking history (e.g. after a cursor rebuild) never duplicates rows.
	const query = `
INSERT OR IGNORE INTO changes (repo, file_path, field, key_val, change_type,
                     old_value, new_value, facets_json, commit_sha, author, committed_at, issue_refs_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = s.db.Exec(query,
		c.Repo,
		c.FilePath,
		c.Field,
		nullableString(c.Key),
		string(c.ChangeType),
		nullableString(c.OldValue),
		nullableString(c.NewValue),
		string(facetsJSON),
		c.CommitSha,
		c.Author,
		c.CommittedAt.UTC().Format(time.RFC3339Nano),
		string(issueRefsJSON),
	)
	if err != nil {
		return fmt.Errorf("store: insert change: %w", err)
	}
	return nil
}

// QueryFeed returns up to limit Changes ordered newest-first by committed_at.
func (s *Store) QueryFeed(limit int) ([]domain.Change, error) {
	const query = `
SELECT repo, file_path, field, key_val, change_type,
       old_value, new_value, facets_json, commit_sha, author, committed_at, issue_refs_json
FROM changes
ORDER BY committed_at DESC
LIMIT ?`

	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("store: query feed: %w", err)
	}
	defer rows.Close()

	var results []domain.Change
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan change: %w", err)
		}
		results = append(results, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rows error: %w", err)
	}
	return results, nil
}

// GetHighWaterMark returns the last persisted commit SHA for the given
// (repo, filePath, field) triple, or an empty string if none has been set yet.
// Keying by field (not just repo+path) lets multiple trackers extracting
// different fields from the same file each resume — and, on first run,
// backfill — independently.
func (s *Store) GetHighWaterMark(repo, filePath, field string) (string, error) {
	const query = `SELECT sha FROM high_water_marks WHERE repo = ? AND file_path = ? AND field = ?`
	var sha string
	err := s.db.QueryRow(query, repo, filePath, field).Scan(&sha)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: get high water mark for %q/%q/%q: %w", repo, filePath, field, err)
	}
	return sha, nil
}

// SetHighWaterMark records or overwrites the high-water-mark commit SHA for
// the given (repo, filePath, field) triple.
func (s *Store) SetHighWaterMark(repo, filePath, field, sha string) error {
	const query = `
INSERT INTO high_water_marks (repo, file_path, field, sha) VALUES (?, ?, ?, ?)
ON CONFLICT(repo, file_path, field) DO UPDATE SET sha = excluded.sha`
	if _, err := s.db.Exec(query, repo, filePath, field, sha); err != nil {
		return fmt.Errorf("store: set high water mark for %q/%q/%q: %w", repo, filePath, field, err)
	}
	return nil
}

// scanChange reads one row from a *sql.Rows cursor into a Change.
func scanChange(rows *sql.Rows) (domain.Change, error) {
	var (
		repo          string
		filePath      string
		field         string
		keyVal        sql.NullString
		changeType    string
		oldValue      sql.NullString
		newValue      sql.NullString
		facetsJSON    string
		commitSha     string
		author        string
		committedAt   string
		issueRefsJSON string
	)

	if err := rows.Scan(
		&repo, &filePath, &field, &keyVal, &changeType,
		&oldValue, &newValue, &facetsJSON, &commitSha, &author, &committedAt, &issueRefsJSON,
	); err != nil {
		return domain.Change{}, err
	}

	ts, err := time.Parse(time.RFC3339Nano, committedAt)
	if err != nil {
		return domain.Change{}, fmt.Errorf("parse committed_at %q: %w", committedAt, err)
	}

	var facets map[string]string
	if err := json.Unmarshal([]byte(facetsJSON), &facets); err != nil {
		return domain.Change{}, fmt.Errorf("unmarshal facets: %w", err)
	}

	var issueRefs []string
	if err := json.Unmarshal([]byte(issueRefsJSON), &issueRefs); err != nil {
		return domain.Change{}, fmt.Errorf("unmarshal issue refs: %w", err)
	}

	c := domain.Change{
		Repo:        repo,
		FilePath:    filePath,
		Field:       field,
		ChangeType:  domain.ChangeType(changeType),
		Facets:      facets,
		CommitSha:   commitSha,
		Author:      author,
		CommittedAt: ts,
		IssueRefs:   issueRefs,
	}
	if keyVal.Valid {
		c.Key = &keyVal.String
	}
	if oldValue.Valid {
		c.OldValue = &oldValue.String
	}
	if newValue.Valid {
		c.NewValue = &newValue.String
	}

	return c, nil
}

// nullableString converts a *string pointer to sql.NullString for SQL binding.
func nullableString(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}
