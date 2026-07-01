// Package filter parses request parameters into a FilterSpec and provides a
// match predicate over facet maps. This module is pure — no I/O, no side
// effects, and no SQL: it only decides what should be included or excluded.
// SQL translation of a FilterSpec lives in a later store-layer slice.
package filter

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
