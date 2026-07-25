// Package changeset (this file): Impact classification. Like Kind and Risk,
// Impact is a pure, query-time projection over an already-assembled
// Changeset — never stored — derived from the semantic-version delta of
// each Change via the versiondelta module. Unlike Risk (a set of
// specific-concern flags, zero-or-more per changeset), Impact is a single,
// always-present tier: every changeset carries exactly one, so the Risk
// column is never blank.
package changeset

import "github.com/dackota/change-tracking-dashboard/internal/versiondelta"

// Impact is a changeset's (or a single change's) impact-tier classification.
type Impact string

const (
	// ImpactMajor is a breaking upgrade (e.g. 1.9.0 -> 2.0.0).
	ImpactMajor Impact = "major"
	// ImpactMinor is new functionality (e.g. 1.20.3 -> 1.21.0).
	ImpactMinor Impact = "minor"
	// ImpactPatch is a fix-level bump (e.g. 10.1.2 -> 10.1.3).
	ImpactPatch Impact = "patch"
	// ImpactOther is anything that is not a comparable forward version bump:
	// an add/remove, a non-semver value, a bare-integer quantity, equal
	// values, or (until the downgrade tier ships) a backwards move. The
	// catch-all that guarantees the column is never blank.
	ImpactOther Impact = "other"
)

// impactRollupOrder is the rollup precedence, most notable first: a
// changeset's tier is the highest-precedence tier among its changes.
var impactRollupOrder = []Impact{ImpactMajor, ImpactMinor, ImpactPatch, ImpactOther}

// ClassifyChangeImpact classifies a single Change by the semver delta
// between its old and new value. A Change missing either value (an add or a
// remove), whose values are not both comparable versions, or whose values
// are equal, classifies as ImpactOther. This is a total function: it never
// panics for any Change.
func ClassifyChangeImpact(c Change) Impact {
	if c.OldValue == nil || c.NewValue == nil {
		return ImpactOther
	}
	delta, ok := versiondelta.Compare(*c.OldValue, *c.NewValue)
	if !ok {
		return ImpactOther
	}
	switch delta {
	case versiondelta.Major:
		return ImpactMajor
	case versiondelta.Minor:
		return ImpactMinor
	case versiondelta.Patch:
		return ImpactPatch
	default:
		// versiondelta.Downgrade, for now: the dedicated downgrade tier
		// arrives in its own slice. A backwards move is deliberately not
		// labelled major here, so it lands on the "not a comparable forward
		// bump" catch-all instead of a misleading intermediate state.
		return ImpactOther
	}
}

// ClassifyImpact rolls a Changeset up to a single Impact tier: the highest
// among its Changes' individual tiers, by impactRollupOrder's precedence. A
// Changeset with no Changes, or with no comparable-version Changes, still
// yields ImpactOther rather than panicking or returning empty — Impact is
// always present, never blank.
func ClassifyImpact(cs Changeset) Impact {
	found := make(map[Impact]struct{}, len(cs.Changes))
	for _, c := range cs.Changes {
		found[ClassifyChangeImpact(c)] = struct{}{}
	}
	for _, tier := range impactRollupOrder {
		if _, ok := found[tier]; ok {
			return tier
		}
	}
	return ImpactOther
}
