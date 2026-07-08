package pollstatus_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
)

// TestRegistry_ExtractFailureCounts_StartsEmpty verifies a fresh Registry
// reports no extract failures for any engine — the poll-health surface must
// not show a phantom failure before any has actually occurred.
func TestRegistry_ExtractFailureCounts_StartsEmpty(t *testing.T) {
	t.Parallel()

	r := pollstatus.New()
	counts := r.ExtractFailureCounts()
	if len(counts) != 0 {
		t.Errorf("ExtractFailureCounts() = %v, want empty", counts)
	}
}

// TestRegistry_RecordExtractFailure_IncrementsPerEngine verifies counts are
// tracked per engine (e.g. "hcl") and accumulate across repeated calls,
// satisfying "HCL parse-failure counts are reported on the ... poll-health
// status surface" (acceptance criterion 9) without conflating failures from
// a different engine (e.g. "jq").
func TestRegistry_RecordExtractFailure_IncrementsPerEngine(t *testing.T) {
	t.Parallel()

	r := pollstatus.New()
	r.RecordExtractFailure("hcl")
	r.RecordExtractFailure("hcl")
	r.RecordExtractFailure("jq")

	counts := r.ExtractFailureCounts()
	if counts["hcl"] != 2 {
		t.Errorf("ExtractFailureCounts()[\"hcl\"] = %d, want 2", counts["hcl"])
	}
	if counts["jq"] != 1 {
		t.Errorf("ExtractFailureCounts()[\"jq\"] = %d, want 1", counts["jq"])
	}
}

// TestRegistry_ExtractFailureCounts_ReturnsIndependentCopy verifies the
// returned map is a snapshot copy: mutating it must not corrupt the
// Registry's internal state, and a later Registry call must not retroactively
// change an already-returned snapshot.
func TestRegistry_ExtractFailureCounts_ReturnsIndependentCopy(t *testing.T) {
	t.Parallel()

	r := pollstatus.New()
	r.RecordExtractFailure("hcl")

	snap1 := r.ExtractFailureCounts()
	snap1["hcl"] = 999 // mutate the caller's copy

	r.RecordExtractFailure("hcl") // now 2, internally

	snap2 := r.ExtractFailureCounts()
	if snap2["hcl"] != 2 {
		t.Errorf("ExtractFailureCounts() after mutation of a prior snapshot = %d, want 2 (unaffected by caller mutation)", snap2["hcl"])
	}
}
