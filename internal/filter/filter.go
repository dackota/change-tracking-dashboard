// Package filter parses request parameters into a FilterSpec and provides a
// match predicate over facet maps. This module is pure — no I/O, no side
// effects, and no SQL: it only decides what should be included or excluded.
// SQL translation of a FilterSpec lives in the store-layer, using the
// Includes/Excludes accessors below.
package filter

import "sort"

// FilterSpec is an immutable filter over Changes: a tri-state facet filter
// (for each facet name, a set of values to include, a set to exclude, or
// both) plus an optional repo scope. Unlike a facet, a repo scope is a
// single distinguished value, not an include/exclude set — R26 asks for "the
// chosen tracked repository" (a single scoping choice), not per-value
// tri-state semantics. The zero value matches everything: no includes to
// fail, no excludes to fire, and no repo scope to violate.
type FilterSpec struct {
	includes map[string]map[string]struct{}
	excludes map[string]map[string]struct{}
	repo     string
}

// Matches reports whether facets satisfies the FilterSpec's facet filter:
// every include filter matches and no exclude filter matches. It does not
// consider the repo scope — callers combine Matches with MatchesRepo (AND)
// to get the full spec's verdict for a given (repo, facets) pair.
func (s FilterSpec) Matches(facets map[string]string) bool {
	for name, values := range s.includes {
		if !facetValueIn(facets, name, values) {
			return false
		}
	}
	for name, values := range s.excludes {
		if facetValueIn(facets, name, values) {
			return false
		}
	}
	return true
}

// Repo returns the repo this spec is scoped to, or "" when no repo scope is
// set (the spec matches any repo).
func (s FilterSpec) Repo() string {
	return s.repo
}

// WithRepo returns a copy of s scoped to repo, leaving s itself unchanged.
// Passing "" clears the scope (matches any repo) — the same as the zero
// value's behavior.
func (s FilterSpec) WithRepo(repo string) FilterSpec {
	return FilterSpec{includes: s.includes, excludes: s.excludes, repo: repo}
}

// MatchesRepo reports whether repo satisfies this spec's repo scope: true
// when no scope is set (Repo() == "" is a no-op) or repo equals the scoped
// repo exactly (case-sensitive, no partial match). Combine with Matches via
// AND — never OR — to get the full spec's verdict for a (repo, facets) pair.
func (s FilterSpec) MatchesRepo(repo string) bool {
	return s.repo == "" || s.repo == repo
}

// Includes returns the include side of the spec as facet name -> sorted
// distinct values. The returned map (and its value slices) is an independent
// copy — mutating it never affects the FilterSpec or any other call's result.
func (s FilterSpec) Includes() map[string][]string {
	return exportValueSets(s.includes)
}

// Excludes returns the exclude side of the spec as facet name -> sorted
// distinct values. The returned map (and its value slices) is an independent
// copy — mutating it never affects the FilterSpec or any other call's result.
func (s FilterSpec) Excludes() map[string][]string {
	return exportValueSets(s.excludes)
}

// exportValueSets converts an internal facet -> value-set map into a
// facet -> sorted-values slice map, copying every value so the result never
// aliases the FilterSpec's internal state.
func exportValueSets(sets map[string]map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(sets))
	for name, values := range sets {
		vals := make([]string, 0, len(values))
		for v := range values {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		out[name] = vals
	}
	return out
}

// facetValueIn reports whether facets carries name with a value present in
// values. A facet absent from the map is never "in" any value set — this is
// the shared rule that makes an include fail on absence and an exclude not
// fire on absence.
func facetValueIn(facets map[string]string, name string, values map[string]struct{}) bool {
	v, ok := facets[name]
	if !ok {
		return false
	}
	_, ok = values[v]
	return ok
}
