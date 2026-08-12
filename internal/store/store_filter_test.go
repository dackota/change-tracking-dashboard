package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// seed inserts a set of Changes into s and returns them. The caller controls
// timestamps so ordering is deterministic.
func seedChanges(t *testing.T, s interface {
	SaveChange(domain.Change) error
}, changes []domain.Change) {
	t.Helper()
	for _, c := range changes {
		if err := s.SaveChange(c); err != nil {
			t.Fatalf("SaveChange: %v", err)
		}
	}
}

var (
	base          = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	changeDevZero = domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.0.0"),
		NewValue:    ptr("1.1.0"),
		Facets:      map[string]string{"tenant": "tenant-zero", "env": "dev", "region": "us-west-2"},
		CommitSha:   "sha-dev-zero",
		Author:      "alice",
		CommittedAt: base,
	}
	changeProdOne = domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-one/prod/eu-west-1/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("2.0.0"),
		NewValue:    ptr("2.1.0"),
		Facets:      map[string]string{"tenant": "tenant-one", "env": "prod", "region": "eu-west-1"},
		CommitSha:   "sha-prod-one",
		Author:      "bob",
		CommittedAt: base.Add(time.Hour),
	}
	changeDevOne = domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-one/dev/us-east-1/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("3.0.0"),
		NewValue:    ptr("3.1.0"),
		Facets:      map[string]string{"tenant": "tenant-one", "env": "dev", "region": "us-east-1"},
		CommitSha:   "sha-dev-one",
		Author:      "carol",
		CommittedAt: base.Add(2 * time.Hour),
	}
)

// TestFacetOptions verifies that FacetOptions returns the union of all facet
// names/values across all stored Changes, with sorted distinct values per key.
func TestFacetOptions(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{changeDevZero, changeProdOne, changeDevOne})

	opts, err := s.FacetOptions()
	if err != nil {
		t.Fatalf("FacetOptions: %v", err)
	}

	// Must have env, tenant, region keys.
	for _, key := range []string{"env", "tenant", "region"} {
		if _, ok := opts[key]; !ok {
			t.Errorf("FacetOptions missing key %q", key)
		}
	}

	// env should have sorted distinct values: ["dev", "prod"]
	envVals := opts["env"]
	if len(envVals) != 2 {
		t.Errorf("env values = %v, want [dev prod]", envVals)
	} else if envVals[0] != "dev" || envVals[1] != "prod" {
		t.Errorf("env values = %v, want [dev prod]", envVals)
	}

	// tenant should have: ["tenant-one", "tenant-zero"] (sorted).
	tenantVals := opts["tenant"]
	if len(tenantVals) != 2 {
		t.Errorf("tenant values = %v, want [tenant-one tenant-zero]", tenantVals)
	} else if tenantVals[0] != "tenant-one" || tenantVals[1] != "tenant-zero" {
		t.Errorf("tenant values = %v, want [tenant-one tenant-zero]", tenantVals)
	}
}

// TestFacetOptions_EmptyDatabase verifies that FacetOptions on an empty store
// returns an empty map without error.
func TestFacetOptions_EmptyDatabase(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	opts, err := s.FacetOptions()
	if err != nil {
		t.Fatalf("FacetOptions: %v", err)
	}

	if len(opts) != 0 {
		t.Errorf("FacetOptions (empty): got %v, want empty map", opts)
	}
}

// TestFacetOptions_ExcludesReservedNames is a table/property test over the
// full reserved-name set {repo, asOf, cursor, limit}: none of them may ever
// survive into FacetOptions()'s returned map, even when a stored Change is
// seeded with a facet keyed with exactly that reserved name. This closes the
// repo-param facet-shadowing defect — an admin-configured facet literally
// named "repo" (or asOf/cursor/limit) must never be offered as a UI checkbox
// or accepted as a facet-filter key, because both consumers (the timeline's
// buildFacetControls and the JSON API's parseChangesetsFilter whitelist)
// derive their known-facet set from this one function.
func TestFacetOptions_ExcludesReservedNames(t *testing.T) {
	t.Parallel()

	reservedNames := []string{"repo", "asOf", "cursor", "limit"}

	s := newTestStore(t)
	for i, name := range reservedNames {
		c := domain.Change{
			Repo:       "apps-repo",
			FilePath:   fmt.Sprintf("file-%d.yaml", i),
			Field:      "f",
			ChangeType: domain.ChangeTypeModified,
			OldValue:   ptr("a"),
			NewValue:   ptr("b"),
			// Each seeded Change carries the reserved name plus a co-located
			// non-reserved facet ("region"), so the test also proves the
			// exclusion is narrowly scoped — a legitimate facet on the same
			// Change must still survive.
			Facets:      map[string]string{name: "hijack-value", "region": "us-west-2"},
			CommitSha:   fmt.Sprintf("sha-reserved-%d", i),
			Author:      "alice",
			CommittedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := s.SaveChange(c); err != nil {
			t.Fatalf("SaveChange(%q): %v", name, err)
		}
	}

	opts, err := s.FacetOptions()
	if err != nil {
		t.Fatalf("FacetOptions: %v", err)
	}

	for _, name := range reservedNames {
		t.Run(name, func(t *testing.T) {
			if vals, ok := opts[name]; ok {
				t.Errorf("FacetOptions returned reserved key %q with values %v, want excluded entirely", name, vals)
			}
		})
	}

	if _, ok := opts["region"]; !ok {
		t.Errorf("FacetOptions missing non-reserved key %q, want present (exclusion must not affect non-reserved facets)", "region")
	}
}
