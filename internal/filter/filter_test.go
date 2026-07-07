package filter_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/dackota/change-tracking-dashboard/internal/filter"
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

// TestIncludesExcludes_ExposeSortedValuesForSQLTranslation verifies that
// Includes() and Excludes() expose the parsed sets as facet name -> sorted
// values slices, so a store-layer SQL translator can iterate them
// deterministically without reaching into FilterSpec's private fields.
func TestIncludesExcludes_ExposeSortedValuesForSQLTranslation(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(
		map[string][]string{
			"env":  {"dev", "prod"},
			"tier": {"-sbx", "-staging"},
		},
		map[string]struct{}{"env": {}, "tier": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	wantIncludes := map[string][]string{"env": {"dev", "prod"}}
	if got := spec.Includes(); !reflect.DeepEqual(got, wantIncludes) {
		t.Errorf("Includes() = %v, want %v", got, wantIncludes)
	}

	wantExcludes := map[string][]string{"tier": {"sbx", "staging"}}
	if got := spec.Excludes(); !reflect.DeepEqual(got, wantExcludes) {
		t.Errorf("Excludes() = %v, want %v", got, wantExcludes)
	}
}

// TestIncludesExcludes_ReturnIndependentCopies verifies that mutating a map
// returned by Includes() or Excludes() does not affect the FilterSpec's
// subsequent behavior or later calls — the accessors must not leak internal
// state by reference.
func TestIncludesExcludes_ReturnIndependentCopies(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(
		map[string][]string{"env": {"dev"}},
		map[string]struct{}{"env": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	got := spec.Includes()
	got["env"][0] = "mutated"
	got["injected"] = []string{"x"}

	again := spec.Includes()
	if again["env"][0] != "dev" {
		t.Errorf("Includes() leaked a mutation from a previous call: got %v", again)
	}
	if _, present := again["injected"]; present {
		t.Errorf("Includes() leaked an injected key from a previous call: got %v", again)
	}
}

// TestFilterSpec_ZeroValue_RepoIsUnset verifies that the zero-value
// FilterSpec (as used everywhere a caller does not scope by repo) reports an
// empty Repo() and matches any repo — a repo scope, unlike a tri-state
// facet, is a single distinguished field rather than an include/exclude set.
func TestFilterSpec_ZeroValue_RepoIsUnset(t *testing.T) {
	t.Parallel()

	var spec filter.FilterSpec
	if got := spec.Repo(); got != "" {
		t.Errorf("Repo() = %q, want empty for the zero-value spec", got)
	}
	if !spec.MatchesRepo("apps-repo") {
		t.Error("MatchesRepo(apps-repo) = false, want true (unset repo scope matches any repo)")
	}
}

// TestWithRepo_ReturnsIndependentCopyScopedToRepo verifies that WithRepo
// returns a new FilterSpec carrying the given repo scope, without mutating
// the receiver — the receiver keeps matching every repo afterward.
func TestWithRepo_ReturnsIndependentCopyScopedToRepo(t *testing.T) {
	t.Parallel()

	base := filter.FilterSpec{}
	scoped := base.WithRepo("apps-repo")

	if got := scoped.Repo(); got != "apps-repo" {
		t.Errorf("scoped.Repo() = %q, want apps-repo", got)
	}
	if got := base.Repo(); got != "" {
		t.Errorf("base.Repo() = %q, want empty — WithRepo must not mutate the receiver", got)
	}
	if !base.MatchesRepo("other-repo") {
		t.Error("base.MatchesRepo(other-repo) = false, want true — the receiver must be unaffected by WithRepo")
	}
}

// TestMatchesRepo_ScopedSpec_MatchesOnlyExactRepo verifies that once a repo
// scope is set, MatchesRepo fires only for an exact match — no partial or
// case-insensitive match.
func TestMatchesRepo_ScopedSpec_MatchesOnlyExactRepo(t *testing.T) {
	t.Parallel()

	spec := filter.FilterSpec{}.WithRepo("apps-repo")

	tests := []struct {
		name string
		repo string
		want bool
	}{
		{"exact match", "apps-repo", true},
		{"different repo", "infra-repo", false},
		{"case differs", "Apps-Repo", false},
		{"prefix only", "apps-repo-extra", false},
		{"empty candidate", "", false},
	}
	for _, tc := range tests {
		if got := spec.MatchesRepo(tc.repo); got != tc.want {
			t.Errorf("%s: MatchesRepo(%q) = %v, want %v", tc.name, tc.repo, got, tc.want)
		}
	}
}

// repoScopeSample is the pool of adversarial repo-scope/candidate strings the
// property tests below draw from: the empty string (the "unset" sentinel),
// plain names, near-miss case/whitespace/prefix variants, a very long value,
// and non-ASCII content.
var repoScopeSample = []string{
	"", "apps-repo", "Apps-Repo", " apps-repo", "apps-repo ", "apps-repo-extra",
	"infra-repo", strings.Repeat("x", 512), "仓库-repo", "apps-repo/sub",
}

// repoScopeCase is a quick.Generator drawing a (scope, candidate) pair from
// repoScopeSample so both the "unset scope" and "exact match" branches are
// exercised often, alongside adversarial near-misses.
type repoScopeCase struct {
	scope     string
	candidate string
}

func (repoScopeCase) Generate(rnd *rand.Rand, size int) reflect.Value {
	return reflect.ValueOf(repoScopeCase{
		scope:     repoScopeSample[rnd.Intn(len(repoScopeSample))],
		candidate: repoScopeSample[rnd.Intn(len(repoScopeSample))],
	})
}

// TestMatchesRepo_Property_UnsetIsNoOpElseExactMatch asserts the repo-scope
// invariant for every generated (scope, candidate) pair: an unset scope
// ("") always matches, and a set scope matches only an exact (case-
// sensitive) candidate — never a partial, case-insensitive, or otherwise
// fuzzy match.
func TestMatchesRepo_Property_UnsetIsNoOpElseExactMatch(t *testing.T) {
	t.Parallel()

	property := func(c repoScopeCase) bool {
		spec := filter.FilterSpec{}.WithRepo(c.scope)
		got := spec.MatchesRepo(c.candidate)
		want := c.scope == "" || c.scope == c.candidate
		return got == want
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Error(err)
	}
}

// TestMatchesRepoAndMatches_Property_ComposeWithANDNeverOR asserts the
// structural contract R27 depends on: a Change (repo, facets) satisfies a
// repo-scoped, facet-filtered FilterSpec only when it satisfies *both* the
// repo scope (MatchesRepo) and the facet predicate (Matches) — never either
// alone (OR). It sweeps combinations where exactly one of the two holds,
// both hold, and neither holds, across generated repo scopes/candidates and
// include-facet values.
func TestMatchesRepoAndMatches_Property_ComposeWithANDNeverOR(t *testing.T) {
	t.Parallel()

	envSample := []string{"", "dev", "prod", "staging"}

	property := func(rc repoScopeCase, includeIdx, candIdx uint8) bool {
		includeEnv := envSample[int(includeIdx)%len(envSample)]
		candEnv := envSample[int(candIdx)%len(envSample)]

		params := map[string][]string{}
		if includeEnv != "" {
			params["env"] = []string{includeEnv}
		}
		spec, err := filter.Parse(params, map[string]struct{}{"env": {}})
		if err != nil {
			return false
		}
		spec = spec.WithRepo(rc.scope)

		repoMatches := spec.MatchesRepo(rc.candidate)
		facetMatches := spec.Matches(map[string]string{"env": candEnv})
		combined := repoMatches && facetMatches

		wantRepo := rc.scope == "" || rc.scope == rc.candidate
		wantFacet := includeEnv == "" || includeEnv == candEnv
		want := wantRepo && wantFacet

		if combined != want {
			return false
		}
		// Never OR: a combined true result requires both to independently
		// hold.
		if combined && !(repoMatches && facetMatches) {
			return false
		}
		if repoMatches != wantRepo || facetMatches != wantFacet {
			return false
		}
		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
		t.Error(err)
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
