package chartdiff_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_SuccessPath_ReturnsOKOutcomeWithUnifiedDiffAndSummary proves
// acceptance criterion 1: when both sides render, Diff returns an OK Outcome
// carrying the unified diff and a summary reflecting what changed.
func TestDiff_SuccessPath_ReturnsOKOutcomeWithUnifiedDiffAndSummary(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	renderer := sequentialManifestRenderer(
		&chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: "app", YAML: "data:\n  version: old\n"}}},
		&chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: "app", YAML: "data:\n  version: new\n"}}},
	)

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{
		RepoName:   "application-config",
		TenantPath: "apps/tenant-a",
		CommitSha:  "commit-sha",
	})

	if outcome.Kind != chartdiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.OK)
	}
	if outcome.Diff.Summary.ManifestsChanged != 1 {
		t.Errorf("Summary.ManifestsChanged = %d, want 1", outcome.Diff.Summary.ManifestsChanged)
	}
	if !strings.Contains(outcome.Diff.Unified, "-  version: old") {
		t.Errorf("Unified diff missing removed line:\n%s", outcome.Diff.Unified)
	}
	if !strings.Contains(outcome.Diff.Unified, "+  version: new") {
		t.Errorf("Unified diff missing added line:\n%s", outcome.Diff.Unified)
	}
}
