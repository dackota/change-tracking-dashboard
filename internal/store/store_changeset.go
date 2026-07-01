package store

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/filter"
)

// ErrInvalidCursor is returned (wrapped) by QueryChangesets when the given
// cursor fails to decode. Callers (e.g. the web layer) can use errors.Is to
// distinguish a caller-input problem (map to HTTP 400) from an underlying
// store failure (map to HTTP 500).
var ErrInvalidCursor = errors.New("store: invalid cursor")

// MaxChangesetPageSize is the store's own hard upper bound on the number of
// Changesets materialized by a single QueryChangesets call, independent of
// whatever limit the caller passes (including limit <= 0, which historically
// meant "unbounded"). This is defense in depth: callers such as the web
// layer already clamp the page size before calling in, but the store must
// not rely on that — a distant asOf with no filter could otherwise force an
// unbounded scan of every matching row in the changes table.
const MaxChangesetPageSize = 500

// ChangesetPage is one page of a QueryChangesets result: the Changesets
// themselves plus an opaque cursor for fetching the next page. NextCursor is
// empty when there is no further page.
type ChangesetPage struct {
	Changesets []changeset.Changeset
	NextCursor string
}

// QueryChangesets returns a page of Changesets — Changes grouped by the
// commit that produced them, via the changeset package's assembly logic —
// whose commit was committed strictly before asOf, matching spec, ordered
// most-recent-first (newest commit first; stable ties broken by CommitSha
// ascending, mirroring changeset.Assemble's tie-break).
//
// cursor is the opaque NextCursor from a previous page, or "" for the first
// page. Passing back NextCursor on each call walks the full result set with
// no gaps or overlaps — a page boundary always lands on a commit boundary,
// so a Changeset is never split across two pages. limit bounds the number of
// Changesets in this page (not the number of underlying Change rows).
func (s *Store) QueryChangesets(asOf time.Time, spec filter.FilterSpec, cursor string, limit int) (ChangesetPage, error) {
	seek, err := decodeCursor(cursor)
	if err != nil {
		return ChangesetPage{}, err
	}

	// Clamp to the store's hard maximum regardless of what the caller asked
	// for — including limit <= 0, which used to mean "unbounded". This is
	// the effective page size the SQL query itself is bounded to fetch
	// (distinct commits), not just the size of the slice returned to the
	// caller.
	effectiveLimit := limit
	if effectiveLimit <= 0 || effectiveLimit > MaxChangesetPageSize {
		effectiveLimit = MaxChangesetPageSize
	}

	// Fetch only the Change rows belonging to the (effectiveLimit + 1)
	// distinct commits needed for this page, from the seek position forward
	// — never the full matching set. Fetching one extra commit lets us
	// detect whether a further page exists without a second round trip.
	// Limiting raw Change rows directly would risk truncating a commit's
	// Changeset mid-way; bounding by distinct commit and joining back for
	// all of that commit's rows guarantees a page boundary always lands on
	// a commit boundary.
	changes, err := s.queryChangesForChangesets(asOf, spec, seek, effectiveLimit+1)
	if err != nil {
		return ChangesetPage{}, err
	}

	sets := changeset.Assemble(changes)

	if len(sets) <= effectiveLimit {
		return ChangesetPage{Changesets: sets, NextCursor: ""}, nil
	}

	page := sets[:effectiveLimit]
	last := page[len(page)-1]
	return ChangesetPage{
		Changesets: page,
		NextCursor: encodeCursor(last.CommittedAt, last.CommitSha),
	}, nil
}

// seekPosition identifies the last Changeset returned by a previous page, in
// the same (committedAt DESC, commitSha ASC) order changeset.Assemble uses.
// A zero seekPosition (from an empty cursor) means "start from the top".
type seekPosition struct {
	committedAt time.Time
	commitSha   string
	active      bool
}

// queryChangesForChangesets fetches the Change rows to be grouped into
// Changesets: strictly before asOf, matching spec's facet constraints, and
// strictly after seek (if active) in changeset.Assemble's sort order —
// bounded to the Change rows belonging to at most commitLimit distinct
// commits, rather than every matching row in the table.
//
// The bound is expressed as a CTE that selects the first commitLimit
// distinct (committed_at, commit_sha) keys — in the same order and under the
// same asOf/seek/filter WHERE clauses as the original full query — then
// joins back to the changes table to fetch matching rows for exactly those
// commits. Limiting distinct commits (not raw rows) is what guarantees a
// commit's Changeset is never split by the bound: either some of its
// matching Change rows are fetched, or none are — the same as the original
// unbounded query's per-row filtering, just scoped to fewer commits.
//
// The facet filter clauses are applied twice: once inside the CTE (to pick
// which distinct commits count towards the page) and again on the outer
// join (to drop that commit's non-matching Change rows). asOf and seek are
// only needed in the CTE — the join to page_commits already restricts the
// outer rows to exactly the commits the CTE selected — but the filter must
// be re-applied on the outer side, since a single commit can carry Changes
// with heterogeneous facets and only the matching ones belong in the page.
func (s *Store) queryChangesForChangesets(asOf time.Time, spec filter.FilterSpec, seek seekPosition, commitLimit int) ([]domain.Change, error) {
	var cteWhere strings.Builder
	cteWhere.WriteString("WHERE committed_at < ?")
	cteParams := []any{asOf.UTC().Format(time.RFC3339Nano)}

	if seek.active {
		// Continue strictly past the last Changeset of the previous page, in
		// the same (committed_at DESC, commit_sha ASC) order: either an
		// earlier commit, or the same commit timestamp with a
		// lexicographically greater SHA (the next tie-break slot).
		cteWhere.WriteString("\nAND (committed_at < ? OR (committed_at = ? AND commit_sha > ?))")
		seekTS := seek.committedAt.UTC().Format(time.RFC3339Nano)
		cteParams = append(cteParams, seekTS, seekTS, seek.commitSha)
	}

	if err := appendFilterClauses(&cteWhere, &cteParams, spec); err != nil {
		return nil, err
	}

	// The outer WHERE re-applies only the facet filter clauses (not asOf/
	// seek, which the join to page_commits already enforces), built fresh so
	// its own param slice stays independent of the CTE's.
	var outerWhere strings.Builder
	var outerParams []any
	if err := appendFilterClauses(&outerWhere, &outerParams, spec); err != nil {
		return nil, err
	}
	// appendFilterClauses always emits clauses prefixed with "\nAND ", built
	// for appending after an existing condition — anchor it to a tautology
	// so the outer WHERE is well-formed whether or not spec has any filters.
	outerWhereClause := "WHERE 1 = 1" + outerWhere.String()

	// page_commits: the distinct (committed_at, commit_sha) keys for exactly
	// the commits this page needs, capped by a bound ? parameter (never
	// string-concatenated). Joining changes back against this CTE (rather
	// than selecting rows directly with a raw row LIMIT) is what keeps every
	// row of a selected commit together; re-applying the filter on the outer
	// SELECT keeps per-Change-row filtering identical to the original
	// unbounded query.
	query := fmt.Sprintf(`WITH page_commits AS (
  SELECT DISTINCT committed_at, commit_sha
  FROM changes
  %s
  ORDER BY committed_at DESC, commit_sha ASC
  LIMIT ?
)
SELECT c.repo, c.file_path, c.field, c.key_val, c.change_type,
       c.old_value, c.new_value, c.facets_json, c.commit_sha, c.author, c.committed_at
FROM changes c
JOIN page_commits p ON p.committed_at = c.committed_at AND p.commit_sha = c.commit_sha
%s
ORDER BY c.committed_at DESC, c.commit_sha ASC`, cteWhere.String(), outerWhereClause)

	// Positional param order must match the "?" order in query: the CTE's
	// WHERE clause, then its LIMIT, then the outer WHERE's filter clause.
	params := append(append([]any{}, cteParams...), commitLimit)
	params = append(params, outerParams...)

	rows, err := s.db.Query(query, params...)
	if err != nil {
		return nil, fmt.Errorf("store: query changesets: %w", err)
	}
	defer rows.Close()

	var results []domain.Change
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan change (changesets): %w", err)
		}
		results = append(results, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rows error (changesets): %w", err)
	}
	return results, nil
}

// GetChangeset looks up a single Changeset by (repo, commitSha) — every
// Change that commit produced, assembled and classified the same way
// QueryChangesets does. found is false (with a nil error) when no Change row
// matches; an unknown commit is a normal "nothing here" outcome, not a store
// failure.
func (s *Store) GetChangeset(repo, commitSha string) (changeset.Changeset, bool, error) {
	const query = `
SELECT repo, file_path, field, key_val, change_type,
       old_value, new_value, facets_json, commit_sha, author, committed_at
FROM changes
WHERE repo = ? AND commit_sha = ?
ORDER BY id ASC`

	rows, err := s.db.Query(query, repo, commitSha)
	if err != nil {
		return changeset.Changeset{}, false, fmt.Errorf("store: query changeset %q/%q: %w", repo, commitSha, err)
	}
	defer rows.Close()

	var changes []domain.Change
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return changeset.Changeset{}, false, fmt.Errorf("store: scan change (changeset detail): %w", err)
		}
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		return changeset.Changeset{}, false, fmt.Errorf("store: rows error (changeset detail): %w", err)
	}

	if len(changes) == 0 {
		return changeset.Changeset{}, false, nil
	}

	sets := changeset.Assemble(changes)
	return sets[0], true, nil
}

// cursorSeparator joins the two fields of an encoded cursor. Chosen because
// neither an RFC3339Nano timestamp nor a git SHA can contain it.
const cursorSeparator = "|"

// encodeCursor builds an opaque cursor string from the last Changeset's sort
// key (committedAt, commitSha) so the next page can resume strictly after it.
func encodeCursor(committedAt time.Time, commitSha string) string {
	raw := committedAt.UTC().Format(time.RFC3339Nano) + cursorSeparator + commitSha
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor parses a cursor produced by encodeCursor. An empty string
// decodes to an inactive seekPosition (start from the top). Any other value
// that fails to parse is rejected with a generic error — a cursor is opaque
// caller state and should never be hand-crafted.
func decodeCursor(cursor string) (seekPosition, error) {
	if cursor == "" {
		return seekPosition{}, nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return seekPosition{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}

	parts := strings.SplitN(string(raw), cursorSeparator, 2)
	if len(parts) != 2 {
		return seekPosition{}, ErrInvalidCursor
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return seekPosition{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}

	return seekPosition{committedAt: ts, commitSha: parts[1], active: true}, nil
}
