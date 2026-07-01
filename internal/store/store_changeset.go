package store

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/filter"
)

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

	// Fetch every matching Change row from the seek position forward (mirrors
	// QueryFilteredFeed's full-scan-then-limit approach), then group into
	// Changesets and slice off one page. Limiting Change rows directly would
	// risk truncating a commit's Changeset mid-way; limiting after grouping
	// guarantees a page boundary always lands on a commit boundary.
	changes, err := s.queryChangesForChangesets(asOf, spec, seek)
	if err != nil {
		return ChangesetPage{}, err
	}

	sets := changeset.Assemble(changes)

	if limit <= 0 || len(sets) <= limit {
		return ChangesetPage{Changesets: sets, NextCursor: ""}, nil
	}

	page := sets[:limit]
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
// strictly after seek (if active) in changeset.Assemble's sort order.
func (s *Store) queryChangesForChangesets(asOf time.Time, spec filter.FilterSpec, seek seekPosition) ([]domain.Change, error) {
	const baseQuery = `
SELECT repo, file_path, field, key_val, change_type,
       old_value, new_value, facets_json, commit_sha, author, committed_at
FROM changes
WHERE committed_at < ?`

	var sb strings.Builder
	sb.WriteString(baseQuery)
	params := []any{asOf.UTC().Format(time.RFC3339Nano)}

	if seek.active {
		// Continue strictly past the last Changeset of the previous page, in
		// the same (committed_at DESC, commit_sha ASC) order: either an
		// earlier commit, or the same commit timestamp with a
		// lexicographically greater SHA (the next tie-break slot).
		sb.WriteString("\nAND (committed_at < ? OR (committed_at = ? AND commit_sha > ?))")
		seekTS := seek.committedAt.UTC().Format(time.RFC3339Nano)
		params = append(params, seekTS, seekTS, seek.commitSha)
	}

	if err := appendFilterClauses(&sb, &params, spec); err != nil {
		return nil, err
	}

	sb.WriteString("\nORDER BY committed_at DESC, commit_sha ASC")

	rows, err := s.db.Query(sb.String(), params...)
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
		return seekPosition{}, fmt.Errorf("store: invalid cursor")
	}

	parts := strings.SplitN(string(raw), cursorSeparator, 2)
	if len(parts) != 2 {
		return seekPosition{}, fmt.Errorf("store: invalid cursor")
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return seekPosition{}, fmt.Errorf("store: invalid cursor")
	}

	return seekPosition{committedAt: ts, commitSha: parts[1], active: true}, nil
}
