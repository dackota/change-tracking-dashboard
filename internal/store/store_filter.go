package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// facetKeyPattern constrains facet key names to a safe identifier charset.
// A facet key is concatenated into the json_extract path expression (a column
// path cannot be bound as a ? parameter), so this guards the store boundary
// against any caller passing an unsafe key — independent of, and in addition to,
// the web layer's whitelist. Legitimate facet keys originate from regex
// named-capture groups, which already satisfy this pattern.
var facetKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// reservedFacetNames are facet-key names that must never be offered as a
// selectable/parseable facet, because they collide with query-param names the
// request layer treats specially: the dedicated repo-scope dropdown ("repo"),
// the impact/risk class filters, and the asOf/since/cursor/limit paging
// params. Without this exclusion, a tracker/extractor whose facet map happens
// to produce one of these keys (e.g. a named capture group literally called
// "repo") would render as a UI checkbox and, server-side, shadow the
// dedicated repo-scope query param — the caller's repeated ?repo=... query
// values collapse to whichever one net/url's Query().Get returns first,
// silently overriding the user's actual repo-dropdown selection with the
// facet-driven value.
//
// This set is the single authority for that vocabulary. The request layer
// reads it through IsReservedFacetName rather than keeping its own copy, so
// adding a reserved query param here closes the facet-shadowing hole on both
// sides at once.
var reservedFacetNames = map[string]struct{}{
	"repo":   {},
	"asOf":   {},
	"cursor": {},
	"impact": {},
	"limit":  {},
	"risk":   {},
	"since":  {},
}

// IsReservedFacetName reports whether name is reserved and therefore never
// eligible as a facet — neither offered by FacetOptions nor accepted as a
// facet filter by a caller parsing a request. See reservedFacetNames.
func IsReservedFacetName(name string) bool {
	_, reserved := reservedFacetNames[name]
	return reserved
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
// Any key in reservedFacetNames (repo, asOf, cursor, limit) is excluded from
// the result even if a stored Change carries a facet with that exact name —
// this is the single chokepoint both the HTML timeline's buildFacetControls
// and the JSON API's parseChangesetsFilter whitelist read from, so a
// reserved name can never be offered as a selectable/parseable facet.
//
// Reading all changes and unioning their facet maps is acceptable at PoC volume.
func (s *Store) FacetOptions() (map[string][]string, error) {
	const query = `SELECT facets_json FROM changes`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("store: query facet options: %w", err)
	}
	defer func() { _ = rows.Close() }()

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
			// Reserved names are never eligible as a facet, regardless of
			// what a tracker/extractor happens to produce — see
			// reservedFacetNames.
			if _, reserved := reservedFacetNames[k]; reserved {
				continue
			}
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
