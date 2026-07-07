package store

import (
	"fmt"
	"time"
)

// RepositoryStats is one tracked repository's aggregated Change activity
// (R4): how many Changes it has recorded, how many of those are chart-kind
// (a basename-Chart.yaml source file, mirroring changeset.ClassifyKind)
// Changes, and when its most recent Change was committed.
type RepositoryStats struct {
	Repo         string
	ChangeCount  int
	ChartChanges int
	LastChangeAt time.Time
}

// RepositoryStats returns per-repository Change aggregates (R4): a
// SQL GROUP BY repo over the changes table, giving each repo's total Change
// count, its count of chart-kind Changes, and its most recent commit time.
// Rows are ordered by Repo ascending — a stable, deterministic order that
// does not depend on insert or poll order — so the Repositories view (and
// any downstream repo filter) renders consistently across calls. An empty
// changes table degrades to a non-nil, zero-length slice rather than nil, so
// callers can drive an empty-state branch by length alone.
//
// The chart-kind classification mirrors changeset.ClassifyKind's basename
// check (filepath.Base(filePath) == "Chart.yaml") without importing that
// package here: file_path is git-style forward-slash-separated throughout
// this codebase, so "the basename is Chart.yaml" is exactly "the path
// equals Chart.yaml, or ends with /Chart.yaml". That basename comparison is
// case-sensitive (Go's == on strings), so the SQL predicate uses GLOB, not
// LIKE: SQLite's LIKE is case-insensitive for ASCII by default and would
// wrongly count "chart.yaml" or "CHART.YAML" as chart-kind, while GLOB is
// always case-sensitive and so agrees with ClassifyKind for every input.
func (s *Store) RepositoryStats() ([]RepositoryStats, error) {
	const query = `
SELECT repo,
       COUNT(*) AS change_count,
       SUM(CASE WHEN file_path GLOB 'Chart.yaml' OR file_path GLOB '*/Chart.yaml' THEN 1 ELSE 0 END) AS chart_changes,
       MAX(committed_at) AS last_change_at
FROM changes
GROUP BY repo
ORDER BY repo ASC`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("store: query repository stats: %w", err)
	}
	defer rows.Close()

	var results []RepositoryStats
	for rows.Next() {
		var (
			repo         string
			changeCount  int
			chartChanges int
			lastChangeAt string
		)
		if err := rows.Scan(&repo, &changeCount, &chartChanges, &lastChangeAt); err != nil {
			return nil, fmt.Errorf("store: scan repository stats: %w", err)
		}

		ts, err := time.Parse(time.RFC3339Nano, lastChangeAt)
		if err != nil {
			return nil, fmt.Errorf("store: parse repository stats last_change_at %q: %w", lastChangeAt, err)
		}

		results = append(results, RepositoryStats{
			Repo:         repo,
			ChangeCount:  changeCount,
			ChartChanges: chartChanges,
			LastChangeAt: ts,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rows error (repository stats): %w", err)
	}

	// Return an empty slice (not nil) when there are no repositories, per
	// this package's existing degrade-to-empty convention (see
	// QueryFilteredFeed).
	if results == nil {
		return []RepositoryStats{}, nil
	}
	return results, nil
}
