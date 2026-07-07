package chartdiff_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
)

// bigManifestYAML returns a synthetic manifest body of n lines, each unique,
// so a real diff (not a trivially-collapsed identical/empty one) is produced
// between two variants of it.
func bigManifestYAML(n int, variant string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line%d: %s-%d\n", i, variant, i)
	}
	return b.String()
}

// TestDiff_OversizedUnifiedDiff_TruncatesButKeepsTrueSummaryCounts proves
// acceptance criterion 6: a tiny MaxUnifiedBytes over a large diff truncates
// the emitted Unified text, while Summary still reports the true totals
// (never the post-truncation counts).
func TestDiff_OversizedUnifiedDiff_TruncatesButKeepsTrueSummaryCounts(t *testing.T) {
	t.Parallel()

	const lineCount = 500
	repo := fixedParentRepo("parent-sha")
	renderer := sequentialManifestRenderer(
		&chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: "app", YAML: bigManifestYAML(lineCount, "old")}}},
		&chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: "app", YAML: bigManifestYAML(lineCount, "new")}}},
	)

	engine, err := chartdiff.NewEngine(chartdiff.Config{MaxUnifiedBytes: 200}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

	if outcome.Kind != chartdiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.OK)
	}
	if !outcome.Diff.Truncated {
		t.Error("Diff.Truncated = false, want true for an oversized diff under a 200-byte ceiling")
	}
	if len(outcome.Diff.Unified) > 200 {
		t.Errorf("len(Unified) = %d, want <= 200 (the configured MaxUnifiedBytes)", len(outcome.Diff.Unified))
	}
	if outcome.Diff.Summary.LinesAdded != lineCount {
		t.Errorf("Summary.LinesAdded = %d, want the true total %d despite truncation", outcome.Diff.Summary.LinesAdded, lineCount)
	}
	if outcome.Diff.Summary.LinesRemoved != lineCount {
		t.Errorf("Summary.LinesRemoved = %d, want the true total %d despite truncation", outcome.Diff.Summary.LinesRemoved, lineCount)
	}
}
