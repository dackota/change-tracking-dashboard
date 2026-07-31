// Package filter (this file): ClassSet, a flat allow-set predicate over a
// closed classification vocabulary (impact tiers, and later risk classes).
//
// This is deliberately a separate type from FilterSpec rather than another
// facet on it. The two answer different questions and have different
// mechanics: a FilterSpec is tri-state include/exclude over arbitrary,
// open-ended facet names and translates into SQL WHERE clauses; a ClassSet
// is a flat allow-set over a small closed vocabulary of values that are
// computed at query time and never stored, so it can never touch SQL. Fusing
// them would put a predicate that cannot be pushed down into a type whose
// whole purpose is being pushed down.
package filter

import (
	"errors"
	"sort"
)

// ErrUnknownClass is returned by ParseClassSet when a value is not in the
// supplied vocabulary. Callers (e.g. the web layer) use errors.Is to map it
// to HTTP 400. It deliberately carries no caller input: the offending value
// is never interpolated, because this error is logged server-side and a
// 400 body must not echo request data back.
var ErrUnknownClass = errors.New("filter: unrecognized class value")

// ClassSet is an immutable allow-set over a classification vocabulary. The
// zero value (and any empty set) is a no-op that allows everything, matching
// how an absent filter param behaves elsewhere in this package — a caller
// that never set a class filter must never have results withheld.
type ClassSet struct {
	allowed map[string]struct{}
}

// NewClassSet builds a ClassSet allowing exactly the given values. Duplicates
// collapse. Passing no values yields the no-op set. NewClassSet does not
// validate values against any vocabulary — that is ParseClassSet's job, since
// only the caller knows which vocabulary applies.
func NewClassSet(values ...string) ClassSet {
	if len(values) == 0 {
		return ClassSet{}
	}
	allowed := make(map[string]struct{}, len(values))
	for _, v := range values {
		allowed[v] = struct{}{}
	}
	return ClassSet{allowed: allowed}
}

// Allows reports whether value passes this filter: always true for an empty
// set, otherwise true only for a member. This is a total function — it
// answers for any string, including "" and values outside the vocabulary the
// set was built from, and never panics.
func (s ClassSet) Allows(value string) bool {
	if len(s.allowed) == 0 {
		return true
	}
	_, ok := s.allowed[value]
	return ok
}

// ParseClassSet builds a ClassSet from request-style repeated values (one
// entry per occurrence of the query param), validated against vocab. Repeated
// values OR together. No values yields the no-op set, exactly as an absent
// param does.
//
// An unrecognized value is rejected with ErrUnknownClass rather than silently
// ignored. This diverges on purpose from how unknown facet params are handled
// elsewhere: a facet vocabulary is open-ended and data-derived, so dropping an
// unknown key is the only sane option, whereas a class vocabulary is closed
// and small. Silently ignoring a typo here would degrade to an unfiltered
// result set, which a consumer would misread as "everything matched" — a far
// worse failure than a rejection. vocab is read-only and never retained.
func ParseClassSet(values []string, vocab map[string]struct{}) (ClassSet, error) {
	if len(values) == 0 {
		return ClassSet{}, nil
	}
	allowed := make(map[string]struct{}, len(values))
	for _, v := range values {
		if _, ok := vocab[v]; !ok {
			return ClassSet{}, ErrUnknownClass
		}
		allowed[v] = struct{}{}
	}
	return ClassSet{allowed: allowed}, nil
}

// Empty reports whether this set is the no-op predicate.
func (s ClassSet) Empty() bool {
	return len(s.allowed) == 0
}

// Values returns the allowed values as a sorted, independent copy — mutating
// it never affects the ClassSet.
func (s ClassSet) Values() []string {
	out := make([]string, 0, len(s.allowed))
	for v := range s.allowed {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
