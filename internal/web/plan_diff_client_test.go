// Package web_test (this file): behavioral coverage for acceptance
// criterion 8's client-side half — clicking a Terraform changeset must fetch
// and mount the plandiff resource-change view exactly as
// loadChartDiffsForChangeset already does for the sibling Chart-diff slot.
// As with feed_pagination_test.go and timeline_feed_rows_test.go, there is
// no browser/DOM test harness in this repo, so this asserts against the
// exact source served at /static/timeline.js.
package web_test

import (
	"strings"
	"testing"
)

// TestTimelineJS_PlanDiffAPIPath_PointsAtPlanDiffEndpoint verifies the
// client fetches the plandiff resource-change view from the same endpoint
// path plan_diff.go serves (GET /api/changesets/detail/plan-diff).
func TestTimelineJS_PlanDiffAPIPath_PointsAtPlanDiffEndpoint(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	if !strings.Contains(body, "'/api/changesets/detail/plan-diff'") {
		t.Errorf("timeline.js does not declare the plan-diff API path; got:\n%s", body)
	}
}

// TestTimelineJS_LoadPlanDiffsForChangeset_QueriesPlanDiffSlots verifies the
// client queries the change-plan-diff-slot elements changeset_detail_render.go's
// terraform-change partial renders — mirroring
// loadChartDiffsForChangeset's identical query against
// .change-helm-diff-slot for the sibling Kind.
func TestTimelineJS_LoadPlanDiffsForChangeset_QueriesPlanDiffSlots(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "loadPlanDiffsForChangeset")

	if !strings.Contains(fn, ".change-plan-diff-slot") {
		t.Errorf("loadPlanDiffsForChangeset does not query .change-plan-diff-slot:\n%s", fn)
	}
	if !strings.Contains(fn, "data-tenant-path") {
		t.Errorf("loadPlanDiffsForChangeset does not read the slot's data-tenant-path attribute:\n%s", fn)
	}
}

// TestTimelineJS_OnFlagClick_LoadsBothChartAndPlanDiffsForEachChangeset
// verifies clicking a changeset wires BOTH the chart-diff and plan-diff
// loaders for its rendered detail panel — a Terraform changeset's slot must
// actually be populated when it's clicked, not merely defined and never
// invoked.
func TestTimelineJS_OnFlagClick_LoadsBothChartAndPlanDiffsForEachChangeset(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "onFlagClick")

	if !strings.Contains(fn, "loadChartDiffsForChangeset(") {
		t.Errorf("onFlagClick no longer wires loadChartDiffsForChangeset:\n%s", fn)
	}
	if !strings.Contains(fn, "loadPlanDiffsForChangeset(") {
		t.Errorf("onFlagClick does not wire loadPlanDiffsForChangeset:\n%s", fn)
	}
}
