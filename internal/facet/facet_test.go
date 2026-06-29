package facet_test

import (
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/facet"
)

// conventionPattern mirrors the standard directory convention described in CONTEXT.md:
// apps/<tenant>/<env>/<region>/...
const conventionPattern = `^apps/(?P<tenant>[^/]+)/(?P<env>[^/]+)/(?P<region>[^/]+)/`

func TestExtractFacets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filePath    string
		pattern     string
		wantFacets  map[string]string
		wantErrKind string // "none" | "compile"
	}{
		{
			name:    "full convention match extracts all named groups",
			filePath: "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			pattern:  conventionPattern,
			wantFacets: map[string]string{
				"tenant": "tenant-zero",
				"env":    "dev",
				"region": "us-west-2",
			},
		},
		{
			name:    "partial match extracts available groups only",
			filePath: "apps/tenant-alpha/prod/Chart.yaml",
			pattern:  conventionPattern,
			// Does not match convention (only 2 segments before Chart.yaml), so no facets
			wantFacets: map[string]string{},
		},
		{
			name:    "no match returns empty map not error",
			filePath: "infra/Chart.yaml",
			pattern:  conventionPattern,
			wantFacets: map[string]string{},
		},
		{
			name:    "empty pattern returns empty map",
			filePath: "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			pattern:  "",
			wantFacets: map[string]string{},
		},
		{
			name:        "invalid regex returns compile error",
			filePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			pattern:     `^apps/(?P<tenant>[invalid`,
			wantErrKind: "compile",
		},
		{
			name:    "pattern with no named groups returns empty map",
			filePath: "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			pattern:  `^apps/`,
			wantFacets: map[string]string{},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fe, err := facet.NewExtractor(tc.pattern)
			if tc.wantErrKind == "compile" {
				if err == nil {
					t.Fatal("expected compile error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewExtractor(%q): unexpected error: %v", tc.pattern, err)
			}

			got := fe.ExtractFacets(tc.filePath)

			if len(got) != len(tc.wantFacets) {
				t.Fatalf("ExtractFacets() = %v (len %d), want %v (len %d)",
					got, len(got), tc.wantFacets, len(tc.wantFacets))
			}
			for k, wantV := range tc.wantFacets {
				if gotV, ok := got[k]; !ok || gotV != wantV {
					t.Errorf("facet[%q] = %q, want %q", k, gotV, wantV)
				}
			}
		})
	}
}
