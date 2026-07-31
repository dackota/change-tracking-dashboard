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
    issue_refs_json TEXT  NOT NULL DEFAULT '[]',
    commit_subject TEXT   NOT NULL DEFAULT ''
);`

// high_water_marks is keyed by (repo, file_path, field), not repo or
// (repo, file_path): a glob tracker fans out across many files, AND several
// trackers can extract different fields from the same file. Each
// (file, field) must resume from its own cursor. Keying only by file path let
// whichever field polled a file first advance the shared cursor to HEAD, so
// every other field on that file saw a non-empty cursor, skipped its first-run
// backfill, and silently never recorded its history — see the per-field
// backfill regression test.
//
// committed_at is the cursor commit's own timestamp, recorded so a resuming
// poll can bound how far back it walks: without it, a walk has no way to know
// where its cursor sits in time and must walk to the root to find it. An empty
// string means "unknown", which is what a row written before this column
// existed reads back as.
const schemaHWM = `
CREATE TABLE IF NOT EXISTS high_water_marks (
    repo         TEXT NOT NULL,
    file_path    TEXT NOT NULL,
    field        TEXT NOT NULL,
    sha          TEXT NOT NULL,
    committed_at TEXT NOT NULL DEFAULT '',
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
	// See #85: pre-existing rows lack a commit subject and fall back to
	// the empty-string default (the web layer falls back to the SHA).
	if err := s.ensureColumn("changes", "commit_subject", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("add changes.commit_subject column: %w", err)
	}
	// See #180: cursors written before this column existed have no timestamp
	// and read back as unknown, which costs those fields one unbounded walk
	// each until their next poll records one.
	if err := s.ensureColumn("high_water_marks", "committed_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("add high_water_marks.committed_at column: %w", err)
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
                     old_value, new_value, facets_json, commit_sha, author, committed_at, issue_refs_json, commit_subject)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

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
		c.Subject,
	)
	if err != nil {
		return fmt.Errorf("store: insert change: %w", err)
	}
	return nil
}

// HighWaterMark is a field's polling cursor: the last commit whose Changes
// were recorded, and when that commit happened.
//
// The timestamp is what lets a resuming poll bound its walk — a walk that
// knows only the cursor's SHA has to walk to the repo root to find it, since
// SHAs carry no ordering. It is the commit's author time, matching the
// timestamp gitsource.WalkCommits bounds on; recording committer time here
// and bounding on author time would truncate histories under skew.
//
// The zero value means "no cursor yet" (first run). A non-empty Sha with a
// zero CommittedAt means "cursor known, its time is not" — a row written
// before the timestamp was persisted; see #180.
type HighWaterMark struct {
	Sha         string
	CommittedAt time.Time
}

// IsZero reports whether no cursor has been recorded yet, i.e. this field has
// never completed a poll and its next one is a first run.
func (m HighWaterMark) IsZero() bool { return m.Sha == "" }

// TimeKnown reports whether this cursor carries a usable timestamp. False for
// a legacy row, whose field must fall back to an unbounded walk until its next
// poll records one.
func (m HighWaterMark) TimeKnown() bool { return !m.CommittedAt.IsZero() }

// GetHighWaterMark returns the polling cursor for the given (repo, filePath,
// field) triple, or the zero HighWaterMark if none has been set yet.
// Keying by field (not just repo+path) lets multiple trackers extracting
// different fields from the same file each resume — and, on first run,
// backfill — independently.
func (s *Store) GetHighWaterMark(repo, filePath, field string) (HighWaterMark, error) {
	const query = `SELECT sha, committed_at FROM high_water_marks WHERE repo = ? AND file_path = ? AND field = ?`
	var (
		sha         string
		committedAt string
	)
	err := s.db.QueryRow(query, repo, filePath, field).Scan(&sha, &committedAt)
	if err == sql.ErrNoRows {
		return HighWaterMark{}, nil
	}
	if err != nil {
		return HighWaterMark{}, fmt.Errorf("store: get high water mark for %q/%q/%q: %w", repo, filePath, field, err)
	}

	m := HighWaterMark{Sha: sha}
	if committedAt != "" {
		t, err := time.Parse(time.RFC3339Nano, committedAt)
		if err != nil {
			// An unparseable timestamp is treated as unknown rather than
			// failing the poll: the cursor itself is still good, and the only
			// cost is one unbounded walk.
			return m, nil
		}
		m.CommittedAt = t
	}
	return m, nil
}

// SetHighWaterMark records or overwrites the polling cursor for the given
// (repo, filePath, field) triple. A zero CommittedAt is stored as unknown.
func (s *Store) SetHighWaterMark(repo, filePath, field string, m HighWaterMark) error {
	const query = `
INSERT INTO high_water_marks (repo, file_path, field, sha, committed_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(repo, file_path, field) DO UPDATE SET sha = excluded.sha, committed_at = excluded.committed_at`

	var committedAt string
	if !m.CommittedAt.IsZero() {
		committedAt = m.CommittedAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := s.db.Exec(query, repo, filePath, field, m.Sha, committedAt); err != nil {
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
		subject       string
	)

	if err := rows.Scan(
		&repo, &filePath, &field, &keyVal, &changeType,
		&oldValue, &newValue, &facetsJSON, &commitSha, &author, &committedAt, &issueRefsJSON, &subject,
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
		Subject:     subject,
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
