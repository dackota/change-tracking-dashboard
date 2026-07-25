// Package web (this file): behavioral coverage for the impact-badge slice of
// the feed row builder — mirrors timeline_feed_rows_test.go's structural-
// source pattern (extractFunctionBody against the exact bytes served at
// /static/timeline.js), since the repo has no browser/DOM test harness.
package web_test

import (
	"strings"
	"testing"
)

// TestTimelineJS_BuildFeedRow_RendersImpactBadge verifies the tracer's
// rendering half: buildFeedRow renders exactly one impact badge per row,
// sourced from cs.impact, into the existing risk cell — no new column, no
// header change.
func TestTimelineJS_BuildFeedRow_RendersImpactBadge(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	if !strings.Contains(fn, "cs.impact") {
		t.Fatalf("buildFeedRow does not read cs.impact:\n%s", fn)
	}
	if !strings.Contains(fn, "impact-badge") {
		t.Errorf("buildFeedRow does not create an impact-badge element:\n%s", fn)
	}

	// Still exactly 6 <td> cells — impact renders into the existing risk
	// cell, it does not add a new column.
	tdCount := strings.Count(fn, "document.createElement('td')")
	if tdCount != 6 {
		t.Errorf("buildFeedRow creates %d <td> cells, want 6 (no new column added):\n%s", tdCount, fn)
	}
}

// TestTimelineJS_BuildFeedRow_ImpactBadgeBeforeRiskBadges verifies the impact
// badge is positioned before any risk badges within the risk cell, per the
// PRD's rendering order.
func TestTimelineJS_BuildFeedRow_ImpactBadgeBeforeRiskBadges(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	riskCellIdx := strings.Index(fn, "feed-cell-risk")
	impactIdx := strings.Index(fn, "impact-badge")
	riskForEachIdx := strings.Index(fn, "cs.risk || []).forEach")

	if riskCellIdx == -1 || impactIdx == -1 || riskForEachIdx == -1 {
		t.Fatalf("could not locate risk cell, impact badge, and risk forEach in buildFeedRow:\n%s", fn)
	}
	if !(riskCellIdx < impactIdx && impactIdx < riskForEachIdx) {
		t.Errorf("impact badge must be built after the risk cell starts but before the risk forEach loop:\n%s", fn)
	}
}

// TestTimelineJS_BuildFeedRow_ImpactBadgeUsesTextContent verifies the impact
// badge follows the same no-innerHTML security invariant as every other
// client-derived cell value.
func TestTimelineJS_BuildFeedRow_ImpactBadgeUsesTextContent(t *testing.T) {
	t.Parallel()

	body := servedTimelineJS(t)
	fn := extractFunctionBody(t, body, "buildFeedRow")

	impactIdx := strings.Index(fn, "impact-badge")
	if impactIdx == -1 {
		t.Fatalf("buildFeedRow does not create an impact-badge element:\n%s", fn)
	}
	// The badge's own construction span: from the impact-badge class
	// assignment through the next riskCell.appendChild, mirroring how the
	// risk badges are appended right after being built.
	span := fn[impactIdx:]
	appendIdx := strings.Index(span, "riskCell.appendChild")
	if appendIdx == -1 {
		t.Fatalf("could not find the impact badge's appendChild call:\n%s", fn)
	}
	span = span[:appendIdx]
	if !strings.Contains(span, ".textContent = cs.impact") {
		t.Errorf("impact badge does not set its text via .textContent = cs.impact:\n%s", span)
	}
}
