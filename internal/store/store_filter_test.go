package store_test

import (
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
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
	base           = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	changeDevZero  = domain.Change{
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

// TestQueryFilteredFeed_SingleFilter verifies that filtering by one facet
// returns only matching Changes (and excludes non-matching ones).
func TestQueryFilteredFeed_SingleFilter(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{changeDevZero, changeProdOne, changeDevOne})

	got, err := s.QueryFilteredFeed(100, map[string]string{"env": "dev"})
	if err != nil {
		t.Fatalf("QueryFilteredFeed: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d changes, want 2 (dev only)", len(got))
	}
	for _, c := range got {
		if c.Facets["env"] != "dev" {
			t.Errorf("change %q has env=%q, want dev", c.CommitSha, c.Facets["env"])
		}
	}
}

// TestQueryFilteredFeed_RejectsUnsafeKey verifies the store boundary guard: a
// facet key that is not a safe identifier (e.g. a SQL-injection attempt) is
// rejected with an error rather than concatenated into the json_extract path.
func TestQueryFilteredFeed_RejectsUnsafeKey(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{changeDevZero, changeProdOne, changeDevOne})

	got, err := s.QueryFilteredFeed(100, map[string]string{"env') = '' OR '1'='1": "x"})
	if err == nil {
		t.Fatalf("expected an error for an unsafe facet key, got nil (returned %d rows)", len(got))
	}
	if got != nil {
		t.Errorf("expected nil result on rejected key, got %d rows", len(got))
	}
}

// TestQueryFilteredFeed_MultipleFiltersAND verifies that two facet constraints
// are ANDed together.
func TestQueryFilteredFeed_MultipleFiltersAND(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{changeDevZero, changeProdOne, changeDevOne})

	got, err := s.QueryFilteredFeed(100, map[string]string{"env": "dev", "tenant": "tenant-one"})
	if err != nil {
		t.Fatalf("QueryFilteredFeed: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d changes, want 1 (dev+tenant-one only)", len(got))
	}
	if got[0].CommitSha != "sha-dev-one" {
		t.Errorf("CommitSha = %q, want sha-dev-one", got[0].CommitSha)
	}
}

// TestQueryFilteredFeed_NoMatch verifies that a filter matching nothing returns
// an empty slice (not nil) without error.
func TestQueryFilteredFeed_NoMatch(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{changeDevZero, changeProdOne})

	got, err := s.QueryFilteredFeed(100, map[string]string{"env": "staging"})
	if err != nil {
		t.Fatalf("QueryFilteredFeed: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("got %d changes, want 0", len(got))
	}
}

// TestQueryFilteredFeed_NoFilters verifies that passing no filters returns all
// changes (backward-compatible with the existing unfiltered behaviour).
func TestQueryFilteredFeed_NoFilters(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChanges(t, s, []domain.Change{changeDevZero, changeProdOne, changeDevOne})

	got, err := s.QueryFilteredFeed(100, nil)
	if err != nil {
		t.Fatalf("QueryFilteredFeed: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d changes, want 3 (all)", len(got))
	}
}

// TestQueryFilteredFeed_LimitAfterFilter verifies that the limit is applied
// AFTER filtering (not before), so matching rows are never dropped by the limit.
func TestQueryFilteredFeed_LimitAfterFilter(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	// Insert 3 "dev" changes and 1 "prod" change.
	devA := changeDevZero
	devA.CommitSha = "dev-a"
	devA.CommittedAt = base

	devB := changeDevZero
	devB.CommitSha = "dev-b"
	devB.CommittedAt = base.Add(time.Minute)

	devC := changeDevZero
	devC.CommitSha = "dev-c"
	devC.CommittedAt = base.Add(2 * time.Minute)

	seedChanges(t, s, []domain.Change{devA, devB, devC, changeProdOne})

	// Limit=2 with env=dev must return 2 dev rows, not drop any dev rows due to
	// a premature SQL LIMIT applied before filtering.
	got, err := s.QueryFilteredFeed(2, map[string]string{"env": "dev"})
	if err != nil {
		t.Fatalf("QueryFilteredFeed: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d changes, want 2 (top 2 dev after full-scan filter)", len(got))
	}
	for _, c := range got {
		if c.Facets["env"] != "dev" {
			t.Errorf("change %q has env=%q, want dev", c.CommitSha, c.Facets["env"])
		}
	}
}

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
	} else {
		if envVals[0] != "dev" || envVals[1] != "prod" {
			t.Errorf("env values = %v, want [dev prod]", envVals)
		}
	}

	// tenant should have: ["tenant-one", "tenant-zero"] (sorted).
	tenantVals := opts["tenant"]
	if len(tenantVals) != 2 {
		t.Errorf("tenant values = %v, want [tenant-one tenant-zero]", tenantVals)
	} else {
		if tenantVals[0] != "tenant-one" || tenantVals[1] != "tenant-zero" {
			t.Errorf("tenant values = %v, want [tenant-one tenant-zero]", tenantVals)
		}
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
