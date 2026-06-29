package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
)

// facetKeyPattern constrains facet key names to a safe identifier charset.
// A facet key is concatenated into the json_extract path expression (a column
// path cannot be bound as a ? parameter), so this guards the store boundary
// against any caller passing an unsafe key — independent of, and in addition to,
// the web layer's whitelist. Legitimate facet keys originate from regex
// named-capture groups, which already satisfy this pattern.
var facetKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// QueryFilteredFeed returns up to limit Changes, filtered by the given facet
// constraints (AND semantics — all constraints must match), ordered newest-first.
//
// The filter is applied across the full dataset before the LIMIT is imposed, so
// matching rows are never silently dropped by an early limit. Passing a nil or
// empty filters map is equivalent to calling QueryFeed — all Changes are returned.
//
// Filter values originate from user input and are passed as SQL parameters
// (? placeholders) — never string-concatenated into the query.
func (s *Store) QueryFilteredFeed(limit int, filters map[string]string) ([]domain.Change, error) {
	// Build the SELECT statement. The WHERE clauses use SQLite json_extract to
	// reach individual keys inside the facets_json blob. Each clause is a
	// separate ? parameter so the driver handles escaping — no string injection.
	const baseQuery = `
SELECT repo, file_path, field, key_val, change_type,
       old_value, new_value, facets_json, commit_sha, author, committed_at
FROM changes`

	var (
		sb     strings.Builder
		params []any
	)
	sb.WriteString(baseQuery)

	if len(filters) > 0 {
		// Sort filter keys so the query is deterministic (easier to test/debug).
		keys := make([]string, 0, len(filters))
		for k := range filters {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		sb.WriteString("\nWHERE ")
		for i, k := range keys {
			// The key is concatenated into the SQL path (not bindable), so it
			// must be a safe identifier. Reject anything else at the boundary.
			if !facetKeyPattern.MatchString(k) {
				return nil, fmt.Errorf("store: invalid facet key %q: must match %s", k, facetKeyPattern)
			}
			if i > 0 {
				sb.WriteString("\n  AND ")
			}
			// json_extract(facets_json, '$.key') = ?  (value bound as a parameter)
			sb.WriteString("json_extract(facets_json, '$.")
			sb.WriteString(k)
			sb.WriteString("') = ?")
			params = append(params, filters[k])
		}
	}

	sb.WriteString("\nORDER BY committed_at DESC\nLIMIT ?")
	params = append(params, limit)

	rows, err := s.db.Query(sb.String(), params...)
	if err != nil {
		return nil, fmt.Errorf("store: query filtered feed: %w", err)
	}
	defer rows.Close()

	var results []domain.Change
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan change (filtered): %w", err)
		}
		results = append(results, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rows error (filtered): %w", err)
	}

	// Return an empty slice (not nil) when there are no results.
	if results == nil {
		return []domain.Change{}, nil
	}
	return results, nil
}

// parseFacetsJSON unmarshals a JSON facets blob into a map[string]string. It is
// a thin wrapper around json.Unmarshal shared by FacetOptions and any future
// callers that need to decode facets without a full scanChange.
func parseFacetsJSON(raw string) (map[string]string, error) {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("unmarshal facets JSON: %w", err)
	}
	return m, nil
}

// FacetOptions returns the available facets as facetName → sorted distinct values,
// derived from the facets actually stored across all Changes. This drives the
// filter controls in the UI.
//
// Reading all changes and unioning their facet maps is acceptable at PoC volume.
func (s *Store) FacetOptions() (map[string][]string, error) {
	const query = `SELECT facets_json FROM changes`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("store: query facet options: %w", err)
	}
	defer rows.Close()

	// Collect distinct values per key using a set (map[value]struct{}).
	type valueSet = map[string]struct{}
	collected := make(map[string]valueSet)

	for rows.Next() {
		var facetsJSON string
		if err := rows.Scan(&facetsJSON); err != nil {
			return nil, fmt.Errorf("store: scan facets_json: %w", err)
		}
		facets, err := parseFacetsJSON(facetsJSON)
		if err != nil {
			return nil, fmt.Errorf("store: parse facets: %w", err)
		}
		for k, v := range facets {
			if _, ok := collected[k]; !ok {
				collected[k] = make(valueSet)
			}
			collected[k][v] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rows error (facet options): %w", err)
	}

	// Convert sets to sorted slices.
	result := make(map[string][]string, len(collected))
	for k, vs := range collected {
		vals := make([]string, 0, len(vs))
		for v := range vs {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		result[k] = vals
	}
	return result, nil
}
