package changesetquery

import (
	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

// impactPredicate adapts an impact allow-set into the opaque predicate the
// store applies after assembly. An empty set yields a nil predicate rather
// than one that always returns true: nil is the store's own "no filtering"
// signal, so an absent impact param costs nothing — no per-changeset
// classification, no page-fill loop iterations beyond the first fetch.
func impactPredicate(impacts filter.ClassSet) store.ChangesetPredicate {
	if impacts.Empty() {
		return nil
	}
	return func(cs changeset.Changeset) bool {
		return impacts.Allows(string(changeset.ClassifyImpact(cs)))
	}
}

// riskPredicate adapts a risk-slug allow-set into a store predicate, closing
// over the rule set to classify against. Taking rules as a parameter rather
// than reading a package default is what makes the filter and the rendered
// risk badges agree by construction: one snapshot is passed to both, so an
// operator's configured rules cannot apply to one and not the other.
//
// A changeset matches when its risk set intersects the requested set, so a
// changeset with no risk classes never matches a non-empty filter — there is
// nothing to intersect with.
func riskPredicate(risks filter.ClassSet, rules []changeset.RiskRule) store.ChangesetPredicate {
	if risks.Empty() {
		return nil
	}
	return func(cs changeset.Changeset) bool {
		for _, r := range changeset.ClassifyRisk(cs, rules) {
			slug, ok := changeset.SlugForRisk(r)
			if ok && risks.Allows(slug) {
				return true
			}
		}
		return false
	}
}

// allPredicates combines predicates with AND, skipping nil ones. It returns
// nil when nothing is left, preserving the store's "no predicate" fast path
// for the common unfiltered request.
//
// AND is the only correct composition here: impact and risk answer different
// questions about the same changeset, so a request naming both is asking for
// changesets satisfying both. Returning the last non-nil predicate instead
// would pass every single-filter test and silently drop a filter whenever two
// are combined.
func allPredicates(preds ...store.ChangesetPredicate) store.ChangesetPredicate {
	active := make([]store.ChangesetPredicate, 0, len(preds))
	for _, p := range preds {
		if p != nil {
			active = append(active, p)
		}
	}
	switch len(active) {
	case 0:
		return nil
	case 1:
		return active[0]
	}
	return func(cs changeset.Changeset) bool {
		for _, p := range active {
			if !p(cs) {
				return false
			}
		}
		return true
	}
}
