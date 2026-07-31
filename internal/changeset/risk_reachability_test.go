package changeset_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
)

// TestProducibleRisks_DefaultRules reports which classes a zero-config
// deployment can ever badge. It is the tracer for making reachability a
// property the code can answer, rather than one an operator discovers by
// getting an empty result.
func TestProducibleRisks_DefaultRules(t *testing.T) {
	t.Parallel()

	got := changeset.ProducibleRisks(changeset.DefaultRiskRules())

	for _, want := range []changeset.Risk{
		changeset.RiskReplaceDestroy,
		changeset.RiskSecurity,
		changeset.RiskCostTripwire,
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("ProducibleRisks(DefaultRiskRules()) is missing %q; got %v", want, got)
		}
	}

	// The shipped defaults deliberately do not carry a major-version-bump
	// rule — impact:major owns that signal, so a major bump earns one badge
	// rather than two saying the same thing (see risk_rules.go and #126).
	if _, ok := got[changeset.RiskMajorVersionBump]; ok {
		t.Errorf("ProducibleRisks(DefaultRiskRules()) includes %q, but the shipped defaults deliberately omit it", changeset.RiskMajorVersionBump)
	}
}

// TestProducibleRisks_IncludesConfiguredRules verifies the answer tracks the
// rule set it is given, not a package default — an operator who configures the
// semverBump recipe from the README makes the class reachable, and
// ProducibleRisks must say so.
func TestProducibleRisks_IncludesConfiguredRules(t *testing.T) {
	t.Parallel()

	configured := append(changeset.DefaultRiskRules(), changeset.RiskRule{
		Name:       "semver-major-bump",
		Risk:       changeset.RiskMajorVersionBump,
		SemverBump: changeset.SemverBumpMajor,
	})

	got := changeset.ProducibleRisks(configured)
	if _, ok := got[changeset.RiskMajorVersionBump]; !ok {
		t.Errorf("ProducibleRisks did not report %q as reachable after the README's recipe was configured; got %v",
			changeset.RiskMajorVersionBump, got)
	}
}

// TestEveryRiskIsEitherDefaultProducedOrDeclaredConfigOnly is the drift guard.
//
// This is the part of #157 that is unambiguously right: RiskMajorVersionBump
// had a constant, a slug, badge rendering, and full classifier support, and
// was still unreachable on a default deployment — and nothing in the codebase
// said whether that was deliberate. It was (see #126), but the only way to
// find out was to read git history.
//
// So every class must now be exactly one of two things: produced by the
// shipped defaults, or explicitly declared as requiring configuration. Adding
// a fifth class forces that decision at the moment of definition and fails
// here until it is made — the same discipline riskSlugs already imposes for
// wire slugs, and for the same reason: a class that compiles, classifies, and
// renders a badge but can never fire is the kind of gap nobody notices until a
// consumer asks why their query returns nothing.
func TestEveryRiskIsEitherDefaultProducedOrDeclaredConfigOnly(t *testing.T) {
	t.Parallel()

	producible := changeset.ProducibleRisks(changeset.DefaultRiskRules())

	for slug := range changeset.RiskSlugs() {
		risk, ok := changeset.RiskFromSlug(slug)
		if !ok {
			t.Fatalf("slug %q has no Risk", slug)
		}

		_, byDefault := producible[risk]
		needsConfig := changeset.RequiresConfiguration(risk)

		switch {
		case byDefault && needsConfig:
			t.Errorf("risk %q (slug %q) is both produced by DefaultRiskRules and declared as requiring configuration — exactly one must hold",
				risk, slug)
		case !byDefault && !needsConfig:
			t.Errorf("risk %q (slug %q) is not produced by any default rule and is not declared as requiring configuration. Either add a rule to DefaultRiskRules, or declare it in risksRequiringConfiguration so operators are told it needs a rule instead of silently getting no results.",
				risk, slug)
		}
	}
}

// TestRequiresConfiguration_UnknownRiskIsNotClaimed keeps the predicate total
// and honest: a value that is not one of the Risk constants is not "requiring
// configuration", it is simply not a risk class.
func TestRequiresConfiguration_UnknownRiskIsNotClaimed(t *testing.T) {
	t.Parallel()

	if changeset.RequiresConfiguration(changeset.Risk("not a real class")) {
		t.Error("RequiresConfiguration claimed an unknown value requires configuration")
	}
}
