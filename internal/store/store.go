// Package store implements the SQLite-backed repository for Changes and
// per-repo high-water-mark commit SHAs. It uses the pure-Go modernc.org/sqlite
// driver so no cgo or external sqlite binary is required.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
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
    committed_at  TEXT    NOT NULL
);`

const schemaHWM = `
CREATE TABLE IF NOT EXISTS high_water_marks (
    repo TEXT PRIMARY KEY,
    sha  TEXT NOT NULL
);`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaChanges); err != nil {
		return fmt.Errorf("create changes table: %w", err)
	}
	if _, err := s.db.Exec(schemaHWM); err != nil {
		return fmt.Errorf("create high_water_marks table: %w", err)
	}
	return nil
}

// SaveChange persists a single Change to the database.
func (s *Store) SaveChange(c domain.Change) error {
	facetsJSON, err := json.Marshal(c.Facets)
	if err != nil {
		return fmt.Errorf("store: marshal facets: %w", err)
	}

	const query = `
INSERT INTO changes (repo, file_path, field, key_val, change_type,
                     old_value, new_value, facets_json, commit_sha, author, committed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

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
       old_value, new_value, facets_json, commit_sha, author, committed_at
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

// GetHighWaterMark returns the last persisted commit SHA for the given repo,
// or an empty string if none has been set yet.
func (s *Store) GetHighWaterMark(repo string) (string, error) {
	const query = `SELECT sha FROM high_water_marks WHERE repo = ?`
	var sha string
	err := s.db.QueryRow(query, repo).Scan(&sha)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: get high water mark for %q: %w", repo, err)
	}
	return sha, nil
}

// SetHighWaterMark records or overwrites the high-water-mark commit SHA for
// the given repo.
func (s *Store) SetHighWaterMark(repo, sha string) error {
	const query = `
INSERT INTO high_water_marks (repo, sha) VALUES (?, ?)
ON CONFLICT(repo) DO UPDATE SET sha = excluded.sha`
	if _, err := s.db.Exec(query, repo, sha); err != nil {
		return fmt.Errorf("store: set high water mark for %q: %w", repo, err)
	}
	return nil
}

// scanChange reads one row from a *sql.Rows cursor into a Change.
func scanChange(rows *sql.Rows) (domain.Change, error) {
	var (
		repo        string
		filePath    string
		field       string
		keyVal      sql.NullString
		changeType  string
		oldValue    sql.NullString
		newValue    sql.NullString
		facetsJSON  string
		commitSha   string
		author      string
		committedAt string
	)

	if err := rows.Scan(
		&repo, &filePath, &field, &keyVal, &changeType,
		&oldValue, &newValue, &facetsJSON, &commitSha, &author, &committedAt,
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

	c := domain.Change{
		Repo:        repo,
		FilePath:    filePath,
		Field:       field,
		ChangeType:  domain.ChangeType(changeType),
		Facets:      facets,
		CommitSha:   commitSha,
		Author:      author,
		CommittedAt: ts,
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
