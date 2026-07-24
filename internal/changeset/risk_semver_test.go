package changeset_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// majorBumpRules is the minimal rule set used by these tests to exercise the
// SemverBump predicate in isolation: a single rule that fires
// RiskMajorVersionBump whenever a change is a major-version jump, with no
// other predicate restricting which field/kind/value it applies to.
func majorBumpRules() []changeset.RiskRule {
	return []changeset.RiskRule{{
		Name:       "semver-major-bump",
		Risk:       changeset.RiskMajorVersionBump,
		SemverBump: changeset.SemverBumpMajor,
	}}
}

// TestClassifyRisk_SemverMajorBump_Fires proves a modified change whose old
// and new values are both semver and whose major component increases is
// classified as a major version bump — the relational predicate the pattern-
// only rules cannot express.
func TestClassifyRisk_SemverMajorBump_Fires(t *testing.T) {
	t.Parallel()

	cs := newChangesetFixture(domain.Change{
		FilePath:   "workloads/app/values.yaml",
		Field:      "imageTags",
		ChangeType: domain.ChangeTypeModified,
		OldValue:   ptr("1.9.0"),
		NewValue:   ptr("2.0.0"),
	})

	got := changeset.ClassifyRisk(cs, majorBumpRules())

	assertRisksEqual(t, got, []changeset.Risk{changeset.RiskMajorVersionBump})
}

// TestClassifyRisk_SemverMajorBump_Boundaries proves what does and does not
// count as a major bump: only a modification where both sides are valid semver
// and the major component strictly increases. Adds/removes, minor/patch bumps,
// non-semver values (Terraform constraints, floating tags), 0.x → 0.y (major
// stays 0), and downgrades all classify to no risk.
func TestClassifyRisk_SemverMajorBump_Boundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  domain.Change
		wantHit bool
	}{
		{
			name:    "major bump fires",
			change:  domain.Change{Field: "imageTags", ChangeType: domain.ChangeTypeModified, OldValue: ptr("9.9.9"), NewValue: ptr("10.0.0")},
			wantHit: true,
		},
		{
			name:    "v-prefixed major bump fires",
			change:  domain.Change{Field: "chartDependencies", ChangeType: domain.ChangeTypeModified, OldValue: ptr("v1.20.3"), NewValue: ptr("v2.0.0")},
			wantHit: true,
		},
		{
			name:    "minor bump does not fire",
			change:  domain.Change{Field: "chartDependencies", ChangeType: domain.ChangeTypeModified, OldValue: ptr("v1.20.3"), NewValue: ptr("v1.21.0")},
			wantHit: false,
		},
		{
			name:    "patch bump does not fire",
			change:  domain.Change{Field: "imageTags", ChangeType: domain.ChangeTypeModified, OldValue: ptr("10.1.2"), NewValue: ptr("10.1.3")},
			wantHit: false,
		},
		{
			name:    "zero-major minor bump does not fire",
			change:  domain.Change{Field: "chartDependencies", ChangeType: domain.ChangeTypeModified, OldValue: ptr("0.1.0"), NewValue: ptr("0.2.0")},
			wantHit: false,
		},
		{
			name:    "added (no old value) does not fire",
			change:  domain.Change{Field: "imageTags", ChangeType: domain.ChangeTypeAdded, NewValue: ptr("2.0.0")},
			wantHit: false,
		},
		{
			name:    "removed (no new value) does not fire",
			change:  domain.Change{Field: "imageTags", ChangeType: domain.ChangeTypeRemoved, OldValue: ptr("1.0.0")},
			wantHit: false,
		},
		{
			name:    "non-semver constraint values do not fire",
			change:  domain.Change{Field: "oci-provider-version", ChangeType: domain.ChangeTypeModified, OldValue: ptr("~>7.0"), NewValue: ptr("~>8.0")},
			wantHit: false,
		},
		{
			name:    "floating tag does not fire",
			change:  domain.Change{Field: "imageTags", ChangeType: domain.ChangeTypeModified, OldValue: ptr("stable"), NewValue: ptr("latest")},
			wantHit: false,
		},
		{
			// A scalar quantity (node count, memory GBs, budget) parses as a
			// bare-integer semver (3 → 3.0.0) but is not a version; raising it
			// must not read as a major version bump.
			name:    "bare integer quantity is not a version bump",
			change:  domain.Change{Field: "node-pool-size", ChangeType: domain.ChangeTypeModified, OldValue: ptr("2"), NewValue: ptr("3")},
			wantHit: false,
		},
		{
			name:    "downgrade does not fire",
			change:  domain.Change{Field: "imageTags", ChangeType: domain.ChangeTypeModified, OldValue: ptr("2.0.0"), NewValue: ptr("1.0.0")},
			wantHit: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.change.FilePath = "workloads/app/values.yaml"
			got := changeset.ClassifyRisk(newChangesetFixture(tc.change), majorBumpRules())

			var want []changeset.Risk
			if tc.wantHit {
				want = []changeset.Risk{changeset.RiskMajorVersionBump}
			}
			assertRisksEqual(t, got, want)
		})
	}
}

// TestDefaultRiskRules_FlagMajorVersionBump proves the shipped defaults flag a
// major version jump on an ordinary tracked field (a container image tag)
// out of the box — the provider-agnostic signal is on by default, not just an
// opt-in config rule.
func TestDefaultRiskRules_FlagMajorVersionBump(t *testing.T) {
	t.Parallel()

	cs := newChangesetFixture(domain.Change{
		FilePath:   "workloads/app/values.yaml",
		Field:      "imageTags",
		ChangeType: domain.ChangeTypeModified,
		OldValue:   ptr("1.4.0"),
		NewValue:   ptr("2.0.0"),
	})

	got := changeset.ClassifyRisk(cs, changeset.DefaultRiskRules())

	assertRisksEqual(t, got, []changeset.Risk{changeset.RiskMajorVersionBump})
}

// TestValidateRiskRules_UnknownSemverBump proves config loaded from an
// external source fails fast on a typo'd bump level rather than silently
// never firing.
func TestValidateRiskRules_UnknownSemverBump(t *testing.T) {
	t.Parallel()

	err := changeset.ValidateRiskRules([]changeset.RiskRule{{
		Name:       "typo",
		Risk:       changeset.RiskMajorVersionBump,
		SemverBump: changeset.SemverBumpLevel("majro"),
	}})
	if err == nil {
		t.Fatal("ValidateRiskRules() = nil, want error for unknown semverBump level")
	}

	if err := changeset.ValidateRiskRules(majorBumpRules()); err != nil {
		t.Fatalf("ValidateRiskRules(valid major rule) = %v, want nil", err)
	}
}
