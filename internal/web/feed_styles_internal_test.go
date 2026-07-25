package web

import (
	"strings"
	"testing"
)

// TestDetailStyles_ImpactBadgeModifiersExist verifies each impact tier gets
// its own CSS modifier class (major red, minor blue, patch green, other
// neutral grey), following the same .risk-badge/.risk-{slug} pattern
// established for Risk, so a tier the CSS doesn't recognize still falls back
// to the neutral .impact-badge base rather than disappearing.
func TestDetailStyles_ImpactBadgeModifiersExist(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		".impact-badge {",
		".impact-badge.impact-major",
		".impact-badge.impact-minor",
		".impact-badge.impact-patch",
	} {
		if !strings.Contains(detailStyles, want) {
			t.Errorf("detailStyles missing %q", want)
		}
	}
}
