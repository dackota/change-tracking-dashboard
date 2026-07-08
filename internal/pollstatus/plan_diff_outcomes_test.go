package pollstatus_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
)

// TestRegistry_PlanDiffOutcomeCounts_StartsEmpty verifies a fresh Registry
// reports no plandiff outcomes for any Kind — the poll-health surface must
// not show a phantom outcome before any plandiff.Engine.Diff call has ever
// happened.
func TestRegistry_PlanDiffOutcomeCounts_StartsEmpty(t *testing.T) {
	t.Parallel()

	r := pollstatus.New()
	counts := r.PlanDiffOutcomeCounts()
	if len(counts) != 0 {
		t.Errorf("PlanDiffOutcomeCounts() = %v, want empty", counts)
	}
}

// TestRegistry_RecordPlanDiffOutcome_IncrementsPerKind verifies counts are
// tracked per Outcome Kind (e.g. "ok", "exceeded-limits") and accumulate
// across repeated calls, satisfying acceptance criterion 9's "plandiff
// outcome counts are reported on the poll-health/status surface" without
// conflating one Kind with another.
func TestRegistry_RecordPlanDiffOutcome_IncrementsPerKind(t *testing.T) {
	t.Parallel()

	r := pollstatus.New()
	r.RecordPlanDiffOutcome("ok")
	r.RecordPlanDiffOutcome("ok")
	r.RecordPlanDiffOutcome("exceeded-limits")

	counts := r.PlanDiffOutcomeCounts()
	if counts["ok"] != 2 {
		t.Errorf(`PlanDiffOutcomeCounts()["ok"] = %d, want 2`, counts["ok"])
	}
	if counts["exceeded-limits"] != 1 {
		t.Errorf(`PlanDiffOutcomeCounts()["exceeded-limits"] = %d, want 1`, counts["exceeded-limits"])
	}
}

// TestRegistry_PlanDiffOutcomeCounts_ReturnsIndependentCopy verifies the
// returned map is a snapshot copy, mirroring ExtractFailureCounts' identical
// guarantee.
func TestRegistry_PlanDiffOutcomeCounts_ReturnsIndependentCopy(t *testing.T) {
	t.Parallel()

	r := pollstatus.New()
	r.RecordPlanDiffOutcome("ok")

	snap1 := r.PlanDiffOutcomeCounts()
	snap1["ok"] = 999

	r.RecordPlanDiffOutcome("ok") // now 2, internally

	snap2 := r.PlanDiffOutcomeCounts()
	if snap2["ok"] != 2 {
		t.Errorf("PlanDiffOutcomeCounts() after mutation of a prior snapshot = %d, want 2 (unaffected by caller mutation)", snap2["ok"])
	}
}
