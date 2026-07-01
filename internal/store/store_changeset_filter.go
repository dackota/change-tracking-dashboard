package store

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/filter"
)

// appendFilterClauses translates spec's includes and excludes into
// parameterized SQL WHERE clauses (each appended with a leading "\nAND ")
// and appends the bound values to params. Facet keys are concatenated into
// the json_extract path (not bindable as a ? parameter), so each key is
// validated against facetKeyPattern before use — the same boundary guard
// QueryFilteredFeed relies on.
//
// Semantics mirror filter.FilterSpec.Matches exactly: every include facet
// must have a matching value (json_extract(...) = ?, OR'd across the
// facet's value set), and no exclude facet may have a matching value. An
// exclude clause explicitly allows the json_extract result to be NULL (facet
// absent) — the whole point being that a Change lacking the excluded facet
// stays visible; a naive "NOT IN" clause alone would filter it out because
// SQL NULL comparisons are NULL, not true.
func appendFilterClauses(sb *strings.Builder, params *[]any, spec filter.FilterSpec) error {
	includes := spec.Includes()
	excludes := spec.Excludes()

	for _, key := range sortedKeys(includes) {
		if err := validateFacetKey(key); err != nil {
			return err
		}
		values := includes[key]

		sb.WriteString("\nAND json_extract(facets_json, '$.")
		sb.WriteString(key)
		sb.WriteString("') IN (")
		sb.WriteString(placeholders(len(values)))
		sb.WriteString(")")
		for _, v := range values {
			*params = append(*params, v)
		}
	}

	for _, key := range sortedKeys(excludes) {
		if err := validateFacetKey(key); err != nil {
			return err
		}
		values := excludes[key]

		// (json_extract(...) IS NULL OR json_extract(...) NOT IN (...)): a
		// facet-absent row (NULL) must survive the exclude, not be dropped by
		// it — SQL's three-valued logic would otherwise make "NOT IN" NULL
		// (falsy) rather than true for an absent facet.
		sb.WriteString("\nAND (json_extract(facets_json, '$.")
		sb.WriteString(key)
		sb.WriteString("') IS NULL OR json_extract(facets_json, '$.")
		sb.WriteString(key)
		sb.WriteString("') NOT IN (")
		sb.WriteString(placeholders(len(values)))
		sb.WriteString("))")
		for _, v := range values {
			*params = append(*params, v)
		}
	}

	return nil
}

// validateFacetKey rejects any facet key that is not a safe identifier,
// since the key is concatenated into the json_extract path expression rather
// than bound as a parameter.
func validateFacetKey(key string) error {
	if !facetKeyPattern.MatchString(key) {
		return fmt.Errorf("store: invalid facet key %q: must match %s", key, facetKeyPattern)
	}
	return nil
}

// sortedKeys returns m's keys sorted ascending, for deterministic clause
// ordering (easier to test/debug — mirrors QueryFilteredFeed's convention).
func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// placeholders returns a comma-separated list of n "?" placeholders.
func placeholders(n int) string {
	ph := make([]string, n)
	for i := range ph {
		ph[i] = "?"
	}
	return strings.Join(ph, ", ")
}
