package plandiff_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// TestConfig_Resolved_ZeroConfigAppliesDefaults verifies the zero Config
// resolves to every documented default.
func TestConfig_Resolved_ZeroConfigAppliesDefaults(t *testing.T) {
	t.Parallel()

	got, err := plandiff.Config{}.Resolved()
	if err != nil {
		t.Fatalf("Resolved: %v", err)
	}

	if got.ParseTimeout != plandiff.DefaultParseTimeout {
		t.Errorf("ParseTimeout = %v, want %v", got.ParseTimeout, plandiff.DefaultParseTimeout)
	}
	if got.ConcurrencyCap != plandiff.DefaultConcurrencyCap {
		t.Errorf("ConcurrencyCap = %d, want %d", got.ConcurrencyCap, plandiff.DefaultConcurrencyCap)
	}
	if got.MaxBlockDepth != plandiff.DefaultMaxBlockDepth {
		t.Errorf("MaxBlockDepth = %d, want %d", got.MaxBlockDepth, plandiff.DefaultMaxBlockDepth)
	}
	if len(got.ForceReplacementAttrs) != len(plandiff.DefaultForceReplacementAttrs) {
		t.Errorf("ForceReplacementAttrs = %v, want %v", got.ForceReplacementAttrs, plandiff.DefaultForceReplacementAttrs)
	}
}

// TestConfig_Resolved_NegativeFieldRejected verifies every field is
// individually validated: an explicitly negative value is always an error,
// even though the zero value would resolve to a default.
func TestConfig_Resolved_NegativeFieldRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  plandiff.Config
	}{
		{"ParseTimeout", plandiff.Config{ParseTimeout: -1}},
		{"ConcurrencyCap", plandiff.Config{ConcurrencyCap: -1}},
		{"MaxUnifiedBytes", plandiff.Config{MaxUnifiedBytes: -1}},
		{"CacheEntries", plandiff.Config{CacheEntries: -1}},
		{"MaxMaterializedBytes", plandiff.Config{MaxMaterializedBytes: -1}},
		{"MaxMaterializedFiles", plandiff.Config{MaxMaterializedFiles: -1}},
		{"MaxMaterializedDepth", plandiff.Config{MaxMaterializedDepth: -1}},
		{"MaxMaterializedNodes", plandiff.Config{MaxMaterializedNodes: -1}},
		{"MaterializeTimeout", plandiff.Config{MaterializeTimeout: -1}},
		{"MaterializeConcurrencyCap", plandiff.Config{MaterializeConcurrencyCap: -1}},
		{"MaxBlockDepth", plandiff.Config{MaxBlockDepth: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tc.cfg.Resolved(); err == nil {
				t.Errorf("Resolved() with negative %s = nil error, want an error", tc.name)
			}
		})
	}
}

// TestConfig_Resolved_ForceReplacementAttrsNeverAliasesPackageDefault proves
// the mutation-isolation invariant a shared package-level default slice
// demands: mutating one resolved Config's ForceReplacementAttrs in place
// must never corrupt DefaultForceReplacementAttrs (or any other Config
// resolved from it) — the hazard an exported mutable package-level slice
// variable would otherwise create.
func TestConfig_Resolved_ForceReplacementAttrsNeverAliasesPackageDefault(t *testing.T) {
	t.Parallel()

	original := append([]string(nil), plandiff.DefaultForceReplacementAttrs...)

	first, err := plandiff.Config{}.Resolved()
	if err != nil {
		t.Fatalf("Resolved: %v", err)
	}
	if len(first.ForceReplacementAttrs) == 0 {
		t.Fatal("ForceReplacementAttrs is empty, cannot exercise the mutation invariant")
	}
	first.ForceReplacementAttrs[0] = "mutated-by-caller"

	second, err := plandiff.Config{}.Resolved()
	if err != nil {
		t.Fatalf("Resolved: %v", err)
	}
	if second.ForceReplacementAttrs[0] != original[0] {
		t.Errorf("a second Resolved() call observed the first call's in-place mutation: got %q, want %q (DefaultForceReplacementAttrs was corrupted)", second.ForceReplacementAttrs[0], original[0])
	}
	for i, v := range plandiff.DefaultForceReplacementAttrs {
		if v != original[i] {
			t.Errorf("DefaultForceReplacementAttrs[%d] = %q, want %q (package-level default was mutated)", i, v, original[i])
		}
	}
}
