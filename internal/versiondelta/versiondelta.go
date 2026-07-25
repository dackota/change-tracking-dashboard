// Package versiondelta answers one question: given an old and a new value
// string, what kind of version change is this? It is the single home for
// everything that decides "what counts as a version" — v-prefix tolerance,
// the bare-integer scalar-quantity guard, prerelease/build metadata, and
// non-semver rejection — so every caller that needs to reason about a
// version bump (risk classification, impact classification) agrees on the
// same definition.
package versiondelta

import (
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Delta classifies the kind of version change between an old and new value.
type Delta string

const (
	// Major is a forward bump whose major component increased.
	Major Delta = "major"
	// Minor is a forward bump whose minor component increased (major held).
	Minor Delta = "minor"
	// Patch is a forward bump whose patch component increased (major/minor held).
	Patch Delta = "patch"
	// Downgrade is any backwards move, regardless of which component decreased.
	Downgrade Delta = "downgrade"
)

// Compare reports what kind of version change moving from oldValue to
// newValue represents. The returned bool is false when the pair is not
// comparable at all: either value fails to parse as a semantic version, one
// side is a bare integer (a scalar quantity like a node count, not a
// version), or the two values are equal (no change to report). Compare is a
// total function: it never panics for any input.
func Compare(oldValue, newValue string) (Delta, bool) {
	oldV, ok := parseVersion(oldValue)
	if !ok {
		return "", false
	}
	newV, ok := parseVersion(newValue)
	if !ok {
		return "", false
	}

	switch {
	case newV.GreaterThan(oldV):
		return forwardDelta(oldV, newV), true
	case newV.LessThan(oldV):
		return Downgrade, true
	default:
		return "", false
	}
}

// forwardDelta classifies a confirmed forward bump (newV > oldV) by the
// highest-order component that changed.
func forwardDelta(oldV, newV *semver.Version) Delta {
	switch {
	case newV.Major() > oldV.Major():
		return Major
	case newV.Minor() > oldV.Minor():
		return Minor
	default:
		return Patch
	}
}

// parseVersion parses s as a semantic version, tolerating a leading "v"
// prefix. A bare integer (no ".") parses as valid semver ("3" -> 3.0.0) but
// is a scalar quantity, not a version, so it is rejected here — the same
// guard changeset.matchesSemverBump applies today.
func parseVersion(s string) (*semver.Version, bool) {
	if !strings.Contains(s, ".") {
		return nil, false
	}
	v, err := semver.NewVersion(s)
	if err != nil {
		return nil, false
	}
	return v, true
}
