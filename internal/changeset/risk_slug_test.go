package changeset_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
)

// TestRiskSlugRoundTrip verifies the slug mapping is total and bidirectional
// for every risk class: each Risk has a URL-clean slug, and that slug maps
// back to exactly the same Risk. The slugs are a wire contract — a consumer's
// saved query breaks if one ever changes — so they are asserted as literals
// here rather than derived from the display values by some rule.
func TestRiskSlugRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		risk changeset.Risk
		slug string
	}{
		{changeset.RiskReplaceDestroy, "replace-destroy"},
		{changeset.RiskSecurity, "security"},
		{changeset.RiskCostTripwire, "cost-tripwire"},
		{changeset.RiskMajorVersionBump, "major-version-bump"},
	}

	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			t.Parallel()

			gotSlug, ok := changeset.SlugForRisk(tt.risk)
			if !ok {
				t.Fatalf("SlugForRisk(%q) reported no slug", tt.risk)
			}
			if gotSlug != tt.slug {
				t.Errorf("SlugForRisk(%q) = %q, want %q", tt.risk, gotSlug, tt.slug)
			}

			gotRisk, ok := changeset.RiskFromSlug(tt.slug)
			if !ok {
				t.Fatalf("RiskFromSlug(%q) reported no risk", tt.slug)
			}
			if gotRisk != tt.risk {
				t.Errorf("RiskFromSlug(%q) = %q, want %q", tt.slug, gotRisk, tt.risk)
			}
		})
	}
}

// TestRiskSlugsAreURLClean verifies the whole point of having slugs: no slug
// contains a character that would need escaping in a query string. The display
// values deliberately do ("replace/destroy", "cost tripwire") — that is why the
// wire uses slugs instead.
func TestRiskSlugsAreURLClean(t *testing.T) {
	t.Parallel()

	for slug := range changeset.RiskSlugs() {
		for _, r := range slug {
			isLower := r >= 'a' && r <= 'z'
			if !isLower && r != '-' {
				t.Errorf("slug %q contains %q; slugs must be lowercase letters and hyphens only", slug, r)
			}
		}
	}
}

// TestRiskSlugLookupsAreTotal verifies both lookups answer for inputs outside
// the vocabulary rather than panicking or returning a bogus zero value that a
// caller might mistake for a real class.
func TestRiskSlugLookupsAreTotal(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"", "nonsense", "SECURITY", "replace/destroy", "cost tripwire"} {
		if got, ok := changeset.RiskFromSlug(slug); ok {
			t.Errorf("RiskFromSlug(%q) = %q, true; want no match", slug, got)
		}
	}

	for _, risk := range []changeset.Risk{"", "not-a-risk", "Security"} {
		if got, ok := changeset.SlugForRisk(risk); ok {
			t.Errorf("SlugForRisk(%q) = %q, true; want no match", risk, got)
		}
	}
}

// TestRiskSlugs_ReturnsIndependentCopy verifies the exported vocabulary cannot
// be used to corrupt the package's own mapping — a caller passes this set to
// filter.ParseClassSet on every request, so aliasing internal state would be a
// live mutation hazard.
func TestRiskSlugs_ReturnsIndependentCopy(t *testing.T) {
	t.Parallel()

	got := changeset.RiskSlugs()
	if len(got) != 4 {
		t.Fatalf("RiskSlugs() has %d entries, want 4", len(got))
	}
	delete(got, "security")
	got["injected"] = struct{}{}

	again := changeset.RiskSlugs()
	if _, ok := again["security"]; !ok {
		t.Error(`mutating the returned set removed "security" from the package's vocabulary`)
	}
	if _, ok := again["injected"]; ok {
		t.Error("mutating the returned set injected a value into the package's vocabulary")
	}
}
