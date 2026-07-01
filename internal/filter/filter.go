// Package filter parses request parameters into a FilterSpec and provides a
// match predicate over facet maps. This module is pure — no I/O, no side
// effects, and no SQL: it only decides what should be included or excluded.
// SQL translation of a FilterSpec lives in the store-layer, using the
// Includes/Excludes accessors below.
package filter

import "sort"

// FilterSpec is an immutable tri-state facet filter: for each facet name it
// may carry a set of values to include, a set of values to exclude, or both.
// The zero value matches everything (no includes to fail, no excludes to fire).
type FilterSpec struct {
	includes map[string]map[string]struct{}
	excludes map[string]map[string]struct{}
}

// Matches reports whether facets satisfies the FilterSpec: every include
// filter matches and no exclude filter matches.
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
