// filter_test.go covers what this package is responsible for: turning request
// params into a FilterSpec, and exposing that spec faithfully through
// Repo/Includes/Excludes. What it means for a Change to *match* a spec is not
// tested here, because it is not decided here — the SQL translation in
// internal/store is the single authority, and its semantics (include OR
// within a facet, AND across facets, exclude not firing on an absent facet,
// exact repo scoping) are asserted against a live database in
// store_changeset_test.go and store_changeset_predicate_property_test.go.
package filter_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/dackota/change-tracking-dashboard/internal/filter"
)

// TestParse_EmptyParams_YieldsAnEmptySpec verifies that parsing an empty
// params map yields a FilterSpec carrying no constraints at all — no includes
// and no excludes, which is what makes the store emit no WHERE clauses and so
// select everything.
func TestParse_EmptyParams_YieldsAnEmptySpec(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(map[string][]string{}, map[string]struct{}{"env": {}, "tier": {}})
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	if got := spec.Includes(); len(got) != 0 {
		t.Errorf("Includes() = %v, want empty for an empty params map", got)
	}
	if got := spec.Excludes(); len(got) != 0 {
		t.Errorf("Excludes() = %v, want empty for an empty params map", got)
	}
	if got := spec.Repo(); got != "" {
		t.Errorf("Repo() = %q, want empty for an empty params map", got)
	}
}

// TestParse_PlainValue_ParsesAsInclude verifies that a plain value (no
// leading "-") lands on the include side of the spec and not the exclude side.
func TestParse_PlainValue_ParsesAsInclude(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(map[string][]string{"tier": {"dev"}}, map[string]struct{}{"tier": {}})
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	if got, want := spec.Includes(), (map[string][]string{"tier": {"dev"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Includes() = %v, want %v", got, want)
	}
	if got := spec.Excludes(); len(got) != 0 {
		t.Errorf("Excludes() = %v, want empty — a plain value is never an exclude", got)
	}
}

// TestParse_DashPrefixedValue_ParsesAsExclude verifies that a value with a
// leading "-" lands on the exclude side, with the "-" stripped, and not on the
// include side.
func TestParse_DashPrefixedValue_ParsesAsExclude(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(map[string][]string{"tier": {"-sbx"}}, map[string]struct{}{"tier": {}})
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	if got, want := spec.Excludes(), (map[string][]string{"tier": {"sbx"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Excludes() = %v, want %v (the leading dash is the marker, not part of the value)", got, want)
	}
	if got := spec.Includes(); len(got) != 0 {
		t.Errorf("Includes() = %v, want empty — a dash-prefixed value is never an include", got)
	}
}

// TestParse_SameFacet_CarriesBothIncludeAndExclude verifies that a single
// facet key can carry both include and exclude values simultaneously (e.g.
// "tier=dev&tier=-sbx"), landing on both sides of the spec independently.
func TestParse_SameFacet_CarriesBothIncludeAndExclude(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(map[string][]string{"tier": {"dev", "-sbx"}}, map[string]struct{}{"tier": {}})
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	if got, want := spec.Includes(), (map[string][]string{"tier": {"dev"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Includes() = %v, want %v", got, want)
	}
	if got, want := spec.Excludes(), (map[string][]string{"tier": {"sbx"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Excludes() = %v, want %v", got, want)
	}
}

// TestParse_MultipleValuesPerFacet_AllSurvive verifies that a facet carrying
// several values keeps all of them, on the correct side — the set the store
// expands into an IN (...) clause.
func TestParse_MultipleValuesPerFacet_AllSurvive(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(
		map[string][]string{"region": {"us-west-2", "us-east-1", "-eu-west-1", "-eu-central-1"}},
		map[string]struct{}{"region": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	if got, want := spec.Includes(), (map[string][]string{"region": {"us-east-1", "us-west-2"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Includes() = %v, want %v", got, want)
	}
	if got, want := spec.Excludes(), (map[string][]string{"region": {"eu-central-1", "eu-west-1"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Excludes() = %v, want %v", got, want)
	}
}

// TestParse_AcrossDifferentFacets_KeepsFacetsSeparate verifies that an include
// on one facet and an exclude on another stay keyed to their own facet names —
// the store emits one clause per facet, so a merge here would silently change
// which Changes are selected.
func TestParse_AcrossDifferentFacets_KeepsFacetsSeparate(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(
		map[string][]string{"env": {"dev"}, "tier": {"-sbx"}},
		map[string]struct{}{"env": {}, "tier": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	if got, want := spec.Includes(), (map[string][]string{"env": {"dev"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Includes() = %v, want %v", got, want)
	}
	if got, want := spec.Excludes(), (map[string][]string{"tier": {"sbx"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Excludes() = %v, want %v", got, want)
	}
}

// TestParse_IsPure_DoesNotAliasCallerInputs verifies that Parse copies its
// inputs: mutating the params slice/map after Parse returns does not change
// the resulting FilterSpec.
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

	// The FilterSpec must still hold exactly what "tier=dev" parsed to.
	if got, want := spec.Includes(), (map[string][]string{"tier": {"dev"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Includes() = %v, want %v — Parse is not pure, it picked up a post-Parse mutation", got, want)
	}
	if got := spec.Excludes(); len(got) != 0 {
		t.Errorf("Excludes() = %v, want empty — Parse picked up the caller's post-Parse append", got)
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
			"env":  {"prod", "dev"},
			"tier": {"-staging", "-sbx"},
		},
		map[string]struct{}{"env": {}, "tier": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	wantIncludes := map[string][]string{"env": {"dev", "prod"}}
	if got := spec.Includes(); !reflect.DeepEqual(got, wantIncludes) {
		t.Errorf("Includes() = %v, want %v (sorted, regardless of param order)", got, wantIncludes)
	}
	wantExcludes := map[string][]string{"tier": {"sbx", "staging"}}
	if got := spec.Excludes(); !reflect.DeepEqual(got, wantExcludes) {
		t.Errorf("Excludes() = %v, want %v (sorted, regardless of param order)", got, wantExcludes)
	}
}

// TestIncludesExcludes_ReturnIndependentCopies verifies that mutating a map
// returned by Includes()/Excludes() cannot reach back into the FilterSpec or
// affect any later call — the spec is immutable to its consumers.
func TestIncludesExcludes_ReturnIndependentCopies(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(
		map[string][]string{"env": {"dev"}, "tier": {"-sbx"}},
		map[string]struct{}{"env": {}, "tier": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}

	inc := spec.Includes()
	inc["env"][0] = "hijacked"
	inc["injected"] = []string{"x"}
	exc := spec.Excludes()
	exc["tier"][0] = "hijacked"
	exc["injected"] = []string{"x"}

	if got, want := spec.Includes(), (map[string][]string{"env": {"dev"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Includes() = %v, want %v — the returned map aliases FilterSpec state", got, want)
	}
	if got, want := spec.Excludes(), (map[string][]string{"tier": {"sbx"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Excludes() = %v, want %v — the returned map aliases FilterSpec state", got, want)
	}
}

// TestFilterSpec_ZeroValue_RepoIsUnset verifies that the zero FilterSpec has an
// empty Repo() — the sentinel the store reads as "emit no repo clause", which
// is what makes an unscoped spec match every repo.
func TestFilterSpec_ZeroValue_RepoIsUnset(t *testing.T) {
	t.Parallel()

	var spec filter.FilterSpec
	if got := spec.Repo(); got != "" {
		t.Errorf("Repo() = %q, want empty for the zero-value spec", got)
	}
}

// TestWithRepo_ReturnsIndependentCopyScopedToRepo verifies that WithRepo
// returns a new FilterSpec carrying the given repo scope, without mutating
// the receiver — the receiver stays unscoped afterward.
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
}

// TestWithRepo_PreservesFacetSets verifies that scoping a spec to a repo
// carries the already-parsed facet sets across unchanged — WithRepo adds a
// scope, it does not rebuild or drop the filter.
func TestWithRepo_PreservesFacetSets(t *testing.T) {
	t.Parallel()

	spec, err := filter.Parse(
		map[string][]string{"env": {"dev"}, "tier": {"-sbx"}},
		map[string]struct{}{"env": {}, "tier": {}},
	)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	scoped := spec.WithRepo("apps-repo")

	if got, want := scoped.Includes(), spec.Includes(); !reflect.DeepEqual(got, want) {
		t.Errorf("scoped.Includes() = %v, want %v", got, want)
	}
	if got, want := scoped.Excludes(), spec.Excludes(); !reflect.DeepEqual(got, want) {
		t.Errorf("scoped.Excludes() = %v, want %v", got, want)
	}
}

// repoScopeSample is the pool of adversarial repo-scope strings the property
// test below draws from: the empty string (the "unset" sentinel), plain names,
// near-miss case/whitespace/prefix variants, a very long value, and non-ASCII
// content.
var repoScopeSample = []string{
	"", "apps-repo", "Apps-Repo", " apps-repo", "apps-repo ", "apps-repo-extra",
	"infra-repo", strings.Repeat("x", 512), "仓库-repo", "apps-repo/sub",
}

// repoScopeCase is a quick.Generator drawing a repo scope from repoScopeSample
// so both the "unset scope" and "set scope" branches are exercised often,
// alongside adversarial near-misses.
type repoScopeCase struct {
	scope string
}

func (repoScopeCase) Generate(rnd *rand.Rand, size int) reflect.Value {
	return reflect.ValueOf(repoScopeCase{scope: repoScopeSample[rnd.Intn(len(repoScopeSample))]})
}

// TestWithRepo_Property_RoundTripsAnyScopeVerbatim asserts that WithRepo
// stores the scope byte-for-byte for every generated value: no trimming, no
// case folding, no normalization. The store compares the scope to a repo with
// SQL "=", so any silent rewrite here would change which Changes are
// selected. R26/R27 depend on "" staying the distinguished unset sentinel.
func TestWithRepo_Property_RoundTripsAnyScopeVerbatim(t *testing.T) {
	t.Parallel()

	property := func(c repoScopeCase) bool {
		return filter.FilterSpec{}.WithRepo(c.scope).Repo() == c.scope
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Error(err)
	}
}
