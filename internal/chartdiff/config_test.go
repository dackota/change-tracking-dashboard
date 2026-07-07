package chartdiff_test

import (
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
)

// TestConfig_Resolved_AppliesDefaultsWhenAllFieldsUnset proves acceptance
// criterion 7: an all-zero Config resolves to the package's conservative
// defaults, so a caller that never sets a bound still gets a safe one.
func TestConfig_Resolved_AppliesDefaultsWhenAllFieldsUnset(t *testing.T) {
	t.Parallel()

	got, err := (chartdiff.Config{}).Resolved()
	if err != nil {
		t.Fatalf("Resolved() error = %v, want nil", err)
	}

	want := chartdiff.Config{
		RenderTimeout:             chartdiff.DefaultRenderTimeout,
		ConcurrencyCap:            chartdiff.DefaultConcurrencyCap,
		MaxUnifiedBytes:           chartdiff.DefaultMaxUnifiedBytes,
		CacheEntries:              chartdiff.DefaultCacheEntries,
		MaxMaterializedBytes:      chartdiff.DefaultMaxMaterializedBytes,
		MaxMaterializedFiles:      chartdiff.DefaultMaxMaterializedFiles,
		MaxMaterializedDepth:      chartdiff.DefaultMaxMaterializedDepth,
		MaxMaterializedNodes:      chartdiff.DefaultMaxMaterializedNodes,
		MaterializeTimeout:        chartdiff.DefaultMaterializeTimeout,
		MaterializeConcurrencyCap: chartdiff.DefaultMaterializeConcurrencyCap,
	}
	if got != want {
		t.Errorf("Resolved() = %+v, want %+v", got, want)
	}
}

// TestConfig_Resolved_PreservesExplicitValues proves a caller-supplied
// positive value survives Resolved() unchanged, while unset fields still
// pick up their defaults alongside it.
func TestConfig_Resolved_PreservesExplicitValues(t *testing.T) {
	t.Parallel()

	got, err := (chartdiff.Config{RenderTimeout: 5 * time.Second, ConcurrencyCap: 2}).Resolved()
	if err != nil {
		t.Fatalf("Resolved() error = %v, want nil", err)
	}

	if got.RenderTimeout != 5*time.Second {
		t.Errorf("RenderTimeout = %v, want 5s (explicit value must survive)", got.RenderTimeout)
	}
	if got.ConcurrencyCap != 2 {
		t.Errorf("ConcurrencyCap = %d, want 2 (explicit value must survive)", got.ConcurrencyCap)
	}
	if got.MaxUnifiedBytes != chartdiff.DefaultMaxUnifiedBytes {
		t.Errorf("MaxUnifiedBytes = %d, want default %d (unset field)", got.MaxUnifiedBytes, chartdiff.DefaultMaxUnifiedBytes)
	}
}

// TestConfig_Resolved_RejectsNonPositiveExplicitValues proves acceptance
// criterion 7's other half: an explicitly negative bound is a validation
// error, never silently accepted or silently defaulted (zero, not negative,
// is what means "unset").
func TestConfig_Resolved_RejectsNonPositiveExplicitValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  chartdiff.Config
	}{
		{"negative RenderTimeout", chartdiff.Config{RenderTimeout: -1}},
		{"negative ConcurrencyCap", chartdiff.Config{ConcurrencyCap: -1}},
		{"negative MaxUnifiedBytes", chartdiff.Config{MaxUnifiedBytes: -1}},
		{"negative CacheEntries", chartdiff.Config{CacheEntries: -1}},
		{"negative MaxMaterializedBytes", chartdiff.Config{MaxMaterializedBytes: -1}},
		{"negative MaxMaterializedFiles", chartdiff.Config{MaxMaterializedFiles: -1}},
		{"negative MaxMaterializedDepth", chartdiff.Config{MaxMaterializedDepth: -1}},
		{"negative MaxMaterializedNodes", chartdiff.Config{MaxMaterializedNodes: -1}},
		{"negative MaterializeTimeout", chartdiff.Config{MaterializeTimeout: -1}},
		{"negative MaterializeConcurrencyCap", chartdiff.Config{MaterializeConcurrencyCap: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := tt.cfg.Resolved(); err == nil {
				t.Errorf("Resolved() error = nil, want a validation error for %s", tt.name)
			}
		})
	}
}
