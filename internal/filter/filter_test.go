package filter_test

import (
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/filter"
)

// TestParse_EmptyParams_MatchesEverything verifies that parsing an empty
// params map yields a FilterSpec whose predicate matches any facet map
// (including an empty one), since there are no includes to fail and no
// excludes to fire.
func TestParse_EmptyParams_MatchesEverything(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(map[string][]string{}, map[string]struct{}{"env": {}, "tier": {}})
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	tests := []map[string]string{
		{},
		{"env": "dev"},
		{"env": "prod", "tier": "sbx"},
	}
	for _, facets := range tests {
		if !spec.Matches(facets) {
			t.Errorf("Matches(%v) = false, want true (empty FilterSpec matches everything)", facets)
		}
	}
}

// TestParse_PlainValue_ParsesAsInclude verifies that a plain value (no
// leading "-") parses as an include filter: the predicate matches only facet
// maps that carry the facet with exactly that value, and fails when the
// facet is absent or has a different value.
func TestParse_PlainValue_ParsesAsInclude(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(map[string][]string{"tier": {"dev"}}, map[string]struct{}{"tier": {}})
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		facets map[string]string
		want   bool
	}{
		{"matching value", map[string]string{"tier": "dev"}, true},
		{"different value", map[string]string{"tier": "prod"}, false},
		{"facet absent", map[string]string{"env": "dev"}, false},
	}
	for _, tc := range tests {
		if got := spec.Matches(tc.facets); got != tc.want {
			t.Errorf("%s: Matches(%v) = %v, want %v", tc.name, tc.facets, got, tc.want)
		}
	}
}

// TestParse_SameFacet_CarriesBothIncludeAndExclude verifies that a single
// facet key can carry both include and exclude values simultaneously (e.g.
// "tier=dev&tier=-sbx"), and both halves apply independently in the predicate.
func TestParse_SameFacet_CarriesBothIncludeAndExclude(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(map[string][]string{"tier": {"dev", "-sbx"}}, map[string]struct{}{"tier": {}})
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		facets map[string]string
		want   bool
	}{
		{"included value matches", map[string]string{"tier": "dev"}, true},
		{"excluded value fails even though not required by include", map[string]string{"tier": "sbx"}, false},
		{"value satisfying neither include nor exclude fails include", map[string]string{"tier": "prod"}, false},
	}
	for _, tc := range tests {
		if got := spec.Matches(tc.facets); got != tc.want {
			t.Errorf("%s: Matches(%v) = %v, want %v", tc.name, tc.facets, got, tc.want)
		}
	}
}

// TestParse_IsPure_DoesNotAliasCallerInputs verifies that Parse copies its
// inputs: mutating the params slice/map after Parse returns does not change
// the resulting FilterSpec's behavior, and mutating a facet map passed to
// Matches afterward does not retroactively change a prior Matches result
// (Matches itself must not retain or mutate its argument).
func TestParse_IsPure_DoesNotAliasCallerInputs(t *testing.T) {
	t.Parallel()

	values := []string{"dev"}
	params := map[string][]string{"tier": values}
	allowed := map[string]struct{}{"tier": {}}

	spec, err := filter.Parse(params, allowed)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	// Mutate the caller's slice/map after Parse returns.
	values[0] = "prod"
	params["tier"] = append(params["tier"], "-sbx")
	delete(allowed, "tier")

	// The FilterSpec must still behave as parsed from the original "tier=dev".
	if !spec.Matches(map[string]string{"tier": "dev"}) {
		t.Error("FilterSpec changed after caller mutated its input slice/map — Parse is not pure")
	}
	if spec.Matches(map[string]string{"tier": "prod"}) {
		t.Error("FilterSpec picked up the caller's post-Parse mutation — Parse is not pure")
	}

	// Mutating the facet map handed to Matches must not affect the spec.
	facets := map[string]string{"tier": "dev"}
	_ = spec.Matches(facets)
	facets["tier"] = "prod"
	if !spec.Matches(map[string]string{"tier": "dev"}) {
		t.Error("a later Matches call was affected by mutating a previously-passed facet map")
	}
}

// TestMatches_MultipleValuesPerFacet_AreORed verifies that when a facet
// carries multiple include values (or multiple exclude values), a single
// matching value in the set is sufficient — values within one facet combine
// with OR, not AND.
func TestMatches_MultipleValuesPerFacet_AreORed(t *testing.T) {
	t.Parallel()

	includeSpec, err := filter.Parse(
		map[string][]string{"region": {"us-west-2", "us-east-1"}},
		map[string]struct{}{"region": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if !includeSpec.Matches(map[string]string{"region": "us-west-2"}) {
		t.Error("include set OR: first value should match")
	}
	if !includeSpec.Matches(map[string]string{"region": "us-east-1"}) {
		t.Error("include set OR: second value should match")
	}
	if includeSpec.Matches(map[string]string{"region": "eu-west-1"}) {
		t.Error("include set OR: value outside the set should not match")
	}

	excludeSpec, err := filter.Parse(
		map[string][]string{"region": {"-us-west-2", "-us-east-1"}},
		map[string]struct{}{"region": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if excludeSpec.Matches(map[string]string{"region": "us-west-2"}) {
		t.Error("exclude set OR: first excluded value should fire")
	}
	if excludeSpec.Matches(map[string]string{"region": "us-east-1"}) {
		t.Error("exclude set OR: second excluded value should fire")
	}
	if !excludeSpec.Matches(map[string]string{"region": "eu-west-1"}) {
		t.Error("exclude set OR: value outside the excluded set should survive")
	}
}

// TestMatches_PositiveAndNegativeAcrossDifferentFacets verifies that an
// include on one facet and an exclude on a different facet combine correctly:
// the match requires env=dev AND NOT tier=sbx.
func TestMatches_PositiveAndNegativeAcrossDifferentFacets(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(
		map[string][]string{"env": {"dev"}, "tier": {"-sbx"}},
		map[string]struct{}{"env": {}, "tier": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		facets map[string]string
		want   bool
	}{
		{"include satisfied, exclude does not fire (tier absent)", map[string]string{"env": "dev"}, true},
		{"include satisfied, exclude does not fire (different tier)", map[string]string{"env": "dev", "tier": "prod"}, true},
		{"include satisfied but exclude fires", map[string]string{"env": "dev", "tier": "sbx"}, false},
		{"include not satisfied even though exclude does not fire", map[string]string{"env": "prod"}, false},
		{"neither include nor exclude condition met", map[string]string{"env": "prod", "tier": "sbx"}, false},
	}
	for _, tc := range tests {
		if got := spec.Matches(tc.facets); got != tc.want {
			t.Errorf("%s: Matches(%v) = %v, want %v", tc.name, tc.facets, got, tc.want)
		}
	}
}

// TestParse_UnknownFacetKey_RejectedWithGenericError verifies the whitelist
// boundary: a params key that is not in the allowed set is rejected with an
// error, and the error message is generic/non-leaking — it must not echo the
// caller-supplied key back (avoids reflecting attacker-controlled input into
// error output).
func TestParse_UnknownFacetKey_RejectedWithGenericError(t *testing.T) {
	t.Parallel()

	const unknownKey = "env'); DROP TABLE changes; --"
	_, err := filter.Parse(
		map[string][]string{unknownKey: {"dev"}},
		map[string]struct{}{"tier": {}},
	)
	if err == nil {
		t.Fatal("Parse: expected an error for an unknown facet key, got nil")
	}
	if strings.Contains(err.Error(), unknownKey) {
		t.Errorf("Parse error echoes the invalid key back (leaking): %v", err)
	}
}

// TestParse_DashPrefixedValue_ParsesAsExclude verifies that a value with a
// leading "-" parses as an exclude filter. The exclude fires only on an
// explicit match (facet present with the excluded value); a facet map that
// lacks the excluded facet entirely survives (the exclude does not fire),
// and a facet map with a different value for that facet also survives.
func TestParse_DashPrefixedValue_ParsesAsExclude(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(map[string][]string{"tier": {"-sbx"}}, map[string]struct{}{"tier": {}})
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		facets map[string]string
		want   bool
	}{
		{"explicit exclude match fires", map[string]string{"tier": "sbx"}, false},
		{"facet absent survives (exclude does not fire)", map[string]string{"env": "dev"}, true},
		{"facet present with different value survives", map[string]string{"tier": "dev"}, true},
		{"empty facet map survives", map[string]string{}, true},
	}
	for _, tc := range tests {
		if got := spec.Matches(tc.facets); got != tc.want {
			t.Errorf("%s: Matches(%v) = %v, want %v", tc.name, tc.facets, got, tc.want)
		}
	}
}
