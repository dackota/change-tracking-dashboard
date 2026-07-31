package store

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
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

// MaxChangesetScan is the maximum number of commits QueryChangesets will
// examine in a single call, independent of how many survive the predicate. It
// is what preserves the "no unbounded scan" property once filtering happens
// after assembly rather than in SQL: without it, a predicate matching nothing
// would walk the entire changes table looking for a page it can never fill.
//
// 5000 is 10x MaxChangesetPageSize. The reasoning: a predicate matching 1 in
// 10 commits still fills a maximum-size 500-changeset page within one call,
// so every realistic impact filter (the least common tier in practice is
// downgrade, well above 1-in-10 of a typical feed) is single-call. Selectivity
// beyond that degrades gracefully into resumption rather than failure — the
// call returns a short page plus a cursor — so the cost of the bound being
// slightly too low is an extra round trip, while the cost of it being too high
// is an unbounded-in-practice query. Cheap to revisit: it changes only how
// much work one call does, never which changesets a full walk yields.
const MaxChangesetScan = 5000

// maxChangesetScanBatch caps a single fetch inside the page-fill loop. The
// loop doubles its batch size on each iteration (starting from the page size)
// so a highly selective predicate converges in a logarithmic number of round
// trips rather than a linear one, and this cap stops one iteration from
// materializing an unreasonable number of Change rows at once.
const maxChangesetScanBatch = 500

// ChangesetPage is one page of a QueryChangesets result: the Changesets
// themselves plus an opaque cursor for fetching the next page, and a count of
// the work done to produce them.
//
// NextCursor is empty when there is no further page — and that is the *only*
// end-of-results signal. Under a predicate, a page can be shorter than the
// requested limit while matches still remain (the scan budget ran out first),
// so a caller must never infer "done" from a short page.
//
// Examined is the number of commits the page-fill loop looked at to produce
// this page, including those the predicate rejected. Compared against
// len(Changesets) it is the diagnostic that makes a pathological filter
// visible: a call that examined 5000 commits to return 3 is a filter that
// needs attention, and without this count that call is indistinguishable in a
// trace from one that examined 3. The store only reports it; recording it on a
// span is the caller's job, since the store holds no context.
type ChangesetPage struct {
	Changesets []changeset.Changeset
	NextCursor string
	Examined   int
}

// ChangesetPredicate is an opaque caller-supplied filter over a fully
// assembled Changeset, applied by QueryChangesets after assembly. A nil
// predicate is a no-op (everything is kept).
//
// This exists because some of a Changeset's properties — its impact tier, its
// risk classes — are query-time projections computed in Go after
// changeset.Assemble and are never stored, so they cannot be expressed as a
// SQL WHERE clause the way a facet filter can. Taking an opaque func keeps
// classification policy entirely outside the store: the store learns only
// "keep or drop", never why.
type ChangesetPredicate func(changeset.Changeset) bool

// keep reports whether cs survives p, treating a nil predicate as a no-op.
func (p ChangesetPredicate) keep(cs changeset.Changeset) bool {
	return p == nil || p(cs)
}

// QueryChangesets returns a page of Changesets — Changes grouped by the
// commit that produced them, via the changeset package's assembly logic —
// whose commit falls within the half-open window w ([w.Since, w.AsOf) — see
// TimeWindow), matching spec, ordered most-recent-first (newest commit
// first; stable ties broken by CommitSha ascending, mirroring
// changeset.Assemble's tie-break).
//
// A window with no Since is unbounded below, which is exactly the
// AsOf-only behavior this method had before TimeWindow existed. A window
// whose Since is at or after its AsOf is empty, not an error: an empty
// window is a normal, handleable outcome in a polling loop, not a caller
// mistake worth failing on.
//
// cursor is the opaque NextCursor from a previous page, or "" for the first
// page. Passing back NextCursor on each call walks the full result set with
// no gaps or overlaps — a page boundary always lands on a commit boundary,
// so a Changeset is never split across two pages. The window is re-applied
// on every page, so paging deep into a window never leaks a Changeset from
// outside it. limit bounds the number of Changesets in this page (not the
// number of underlying Change rows).
//
// pred is an optional post-assembly filter (see ChangesetPredicate); pass nil
// for no class filtering, which leaves behavior identical to before the
// parameter existed. With a predicate, the page is filled by looping — fetch,
// assemble, filter, advance the seek past everything examined — so a filtered
// page is as full as an unfiltered one while matches remain, rather than
// arriving pre-punctured by rejections. That loop is bounded by
// MaxChangesetScan; when the budget runs out mid-page the result is a short
// page *with* a cursor, which is why NextCursor and not page length is the
// end-of-results signal.
func (s *Store) QueryChangesets(w TimeWindow, spec filter.FilterSpec, pred ChangesetPredicate, cursor string, limit int) (ChangesetPage, error) {
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

	// The page-fill loop. Each iteration fetches the Change rows belonging to
	// a bounded number of distinct commits from the current seek position,
	// assembles them, and keeps the survivors of pred. Limiting raw Change
	// rows directly would risk truncating a commit's Changeset mid-way;
	// bounding by distinct commit and joining back for all of that commit's
	// rows guarantees a page boundary always lands on a commit boundary — and
	// because each iteration assembles a whole number of commits, that stays
	// true across iterations too.
	//
	// The loop targets effectiveLimit+1 survivors rather than effectiveLimit,
	// for the same reason the pre-filter implementation fetched one extra
	// commit: finding one more survivor than the page holds proves a further
	// page exists, so an exactly-full final page is not followed by a
	// spurious empty one.
	//
	// It exits on any of three conditions, which are exactly the three ways a
	// page can end: enough survivors found, the underlying result set
	// exhausted, or the scan budget spent.
	var kept []changeset.Changeset
	var lastExamined changeset.Changeset
	examined := 0
	exhausted := false
	batch := effectiveLimit + 1

	for len(kept) <= effectiveLimit && !exhausted && examined < MaxChangesetScan {
		// Never examine more commits than the budget still allows, so the
		// bound holds regardless of batch sizing.
		if remaining := MaxChangesetScan - examined; batch > remaining {
			batch = remaining
		}

		changes, err := s.queryChangesForChangesets(w, spec, seek, batch)
		if err != nil {
			return ChangesetPage{}, err
		}

		sets := changeset.Assemble(changes)
		if len(sets) < batch {
			// Fewer distinct commits came back than the bound allowed, so
			// there is nothing left beyond them.
			exhausted = true
		}

		for _, cs := range sets {
			examined++
			lastExamined = cs
			if pred.keep(cs) {
				kept = append(kept, cs)
			}
			if len(kept) > effectiveLimit {
				break
			}
		}

		if len(sets) > 0 {
			// Advance past the last commit *examined*, not the last one kept:
			// rejected commits have already been judged and must never be
			// re-fetched, which is what keeps a filtered walk linear.
			seek = seekPosition{
				committedAt: lastExamined.CommittedAt,
				commitSha:   lastExamined.CommitSha,
				active:      true,
			}
		}

		// Grow geometrically so a highly selective predicate converges in a
		// logarithmic number of round trips instead of a linear one.
		if batch < maxChangesetScanBatch {
			batch *= 2
			if batch > maxChangesetScanBatch {
				batch = maxChangesetScanBatch
			}
		}
	}

	page := ChangesetPage{Examined: examined}

	switch {
	case len(kept) > effectiveLimit:
		// One survivor beyond the page proves more exist. Resume from the
		// last changeset actually returned, so the surplus is not skipped.
		page.Changesets = kept[:effectiveLimit]
		last := page.Changesets[len(page.Changesets)-1]
		page.NextCursor = encodeCursor(last.CommittedAt, last.CommitSha)
	case exhausted:
		// The result set ran out: this page is the end of the walk.
		page.Changesets = kept
	default:
		// The scan budget was spent with a page that may be short. Returning
		// a cursor is mandatory here: the page is not the end of the walk, and
		// a short page is not itself a "no more results" signal — the absence
		// of a cursor is. Resuming from the last commit examined means the
		// budget already spent is never re-spent.
		page.Changesets = kept
		page.NextCursor = encodeCursor(lastExamined.CommittedAt, lastExamined.CommitSha)
	}

	return page, nil
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
// Changesets: within the half-open window w, matching spec's facet
// constraints, and strictly after seek (if active) in
// changeset.Assemble's sort order —
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
// join (to drop that commit's non-matching Change rows). The window and
// seek are only needed in the CTE — the join to page_commits already
// restricts the outer rows to exactly the commits the CTE selected — but the
// filter must be re-applied on the outer side, since a single commit can
// carry Changes with heterogeneous facets and only the matching ones belong
// in the page.
//
// Both window bounds are pushed into the CTE's WHERE rather than applied as
// a post-filter, so they constrain which distinct commits count towards the
// page. Applying Since after the fact would let it silently shrink pages.
func (s *Store) queryChangesForChangesets(w TimeWindow, spec filter.FilterSpec, seek seekPosition, commitLimit int) ([]domain.Change, error) {
	var cteWhere strings.Builder
	cteWhere.WriteString("WHERE committed_at < ?")
	cteParams := []any{w.AsOf.UTC().Format(time.RFC3339Nano)}

	// The inclusive lower bound, mirroring TimeWindow.Contains. A zero Since
	// emits no clause at all — unbounded below, as every caller behaved
	// before the window existed.
	if !w.Since.IsZero() {
		cteWhere.WriteString("\nAND committed_at >= ?")
		cteParams = append(cteParams, w.Since.UTC().Format(time.RFC3339Nano))
	}

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
       c.old_value, c.new_value, c.facets_json, c.commit_sha, c.author, c.committed_at, c.issue_refs_json, c.commit_subject
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
       old_value, new_value, facets_json, commit_sha, author, committed_at, issue_refs_json, commit_subject
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
