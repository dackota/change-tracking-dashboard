// Package store (this file): TimeWindow, the half-open commit-time window
// that bounds a changeset query. Kept separate from the SQL that applies it
// so the boundary invariant — inclusive lower, exclusive upper — is stated
// and tested in one pure place, independent of any database.
package store

import "time"

// TimeWindow is a half-open window over commit time: [Since, AsOf).
//
// Since is an inclusive lower bound; the zero value means "no lower bound",
// which is how every caller predating the window behaved. AsOf is an
// exclusive upper bound and is always applied — a zero AsOf therefore
// selects nothing, exactly as passing a zero asOf did before this type
// existed.
//
// Half-open is the load-bearing choice: it makes consecutive windows tile
// the timeline exactly once, so an incremental consumer can use one
// request's AsOf as the next request's Since with no gaps, no duplicates,
// and no timestamp arithmetic.
type TimeWindow struct {
	// Since is the inclusive lower bound. The zero value means unbounded
	// below.
	Since time.Time
	// AsOf is the exclusive upper bound. Always applied.
	AsOf time.Time
}

// Contains reports whether t falls within the window: at or after Since (when
// Since is set) and strictly before AsOf.
func (w TimeWindow) Contains(t time.Time) bool {
	if !t.Before(w.AsOf) {
		return false
	}
	return w.Since.IsZero() || !t.Before(w.Since)
}
