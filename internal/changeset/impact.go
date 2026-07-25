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
	// ImpactDowngrade is a version moving backwards (a rollback), regardless
	// of which component decreased (e.g. 2.0.0 -> 1.9.0, or 1.9.3 -> 1.9.1).
	ImpactDowngrade Impact = "downgrade"
	// ImpactOther is anything that is not a comparable version change at
	// all: an add/remove, a non-semver value, a bare-integer quantity, or
	// equal values. The catch-all that guarantees the column is never blank.
	ImpactOther Impact = "other"
)

// impactRollupOrder is the rollup precedence, most notable first: a
// changeset's tier is the highest-precedence tier among its changes.
//
// Precedence decision (confirmed): major > downgrade > minor > patch >
// other. A rollback is more notable than a routine forward minor/patch
// bump, but a major version jump is still the headline for a changeset that
// contains both. Cheap to revisit later since Impact is computed at read
// time, never stored.
var impactRollupOrder = []Impact{ImpactMajor, ImpactDowngrade, ImpactMinor, ImpactPatch, ImpactOther}

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
	case versiondelta.Downgrade:
		return ImpactDowngrade
	default:
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
