// Package filter parses request parameters into a FilterSpec: the immutable,
// storage-independent statement of which Changes a request asks for. This
// module is pure — no I/O, no side effects, and no SQL: it only carries what
// should be included or excluded.
//
// A FilterSpec is not self-applying. The one authority on what it means to
// match is the SQL translation in internal/store (appendFilterClauses), which
// reads the spec through the Repo/Includes/Excludes accessors below. Adding a
// second, in-process interpretation here would create two definitions of
// matching that only a comment could hold together — notably around facet
// absence, where SQL's three-valued logic makes the exclude case subtle.
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
