package chartdiff_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
)

// TestDiff_SynchronousPanic_NeverEscapesAndNeverLeaksTempDir is the
// class-level invariant test for the temp-dir cleanup lifecycle: for EVERY
// termination of a per-side materialize+render — including a panic —
// Engine.Diff must (1) never let the panic escape (it returns a classified
// CouldNotRender Outcome instead, upholding "Diff never panics", see
// engine.go) and (2) never leak the exclusive temp dir that side's
// materialize created.
//
// The prior fix closed this for a panic inside the render goroutine
// (render_panic_test.go). This test proves the same invariant holds for a
// panic in repo.MaterializeSubtreeBounded itself — which runs synchronously,
// not in a goroutine — on BOTH the old side and the new side, since
// MaterializeSubtreeBounded's own doc says it walks untrusted,
// attacker-controlled repository content: a go-git panic on a corrupt or
// adversarial object is in threat model. It also re-asserts the render-panic
// case so the whole family is covered by one test, table-driven.
func TestDiff_SynchronousPanic_NeverEscapesAndNeverLeaksTempDir(t *testing.T) {
	// Redirect os.MkdirTemp's base dir (it honors $TMPDIR on unix) into a
	// test-controlled, otherwise-empty directory so a leaked "chartdiff-*"
	// entry can be detected reliably after Diff returns. t.Setenv forbids
	// t.Parallel, so this test (and its subtests) run sequentially.
	tmpBase := t.TempDir()
	t.Setenv("TMPDIR", tmpBase)

	tests := []struct {
		name          string
		materializeFn func(sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds) error
		renderFn      func(callN int, chartDir string, values map[string]interface{}) (*chartrender.Result, error)
	}{
		{
			name: "materialize panics on old side (first call)",
			materializeFn: func(sha, _, _ string, _ gitsource.MaterializeBounds) error {
				if sha == "parent-sha" {
					panic("boom: simulated go-git panic materializing old side")
				}
				return nil
			},
		},
		{
			name: "materialize panics on new side (second call)",
			materializeFn: func(sha, _, _ string, _ gitsource.MaterializeBounds) error {
				if sha == "commit-sha" {
					panic("boom: simulated go-git panic materializing new side")
				}
				return nil
			},
		},
		{
			name: "render panics",
			renderFn: func(int, string, map[string]interface{}) (*chartrender.Result, error) {
				panic("boom: simulated Helm SDK panic on a hostile chart")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := fixedParentRepo("parent-sha")
			repo.materializeFn = tt.materializeFn
			renderer := &fakeRenderer{fn: tt.renderFn}

			engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}

			// engine.Diff itself must not panic — if the fix is missing, this
			// call crashes the test (and, in production, the dashboard
			// process) instead of returning.
			outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "commit-sha"})

			if outcome.Kind != chartdiff.CouldNotRender {
				t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.CouldNotRender)
			}

			assertNoLeakedChartdiffTempDirs(t, tmpBase)
		})
	}
}

// assertNoLeakedChartdiffTempDirs fails the test if base contains any entry
// created by newExclusiveTempDir (os.MkdirTemp("", "chartdiff-*")) — proving
// no per-side materialize temp dir survived past Diff's return, on any
// termination path.
func assertNoLeakedChartdiffTempDirs(t *testing.T, base string) {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read temp base %q: %v", base, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "chartdiff-") {
			t.Errorf("leaked temp dir %q under %q after Diff returned, want it cleaned up on every termination path including a panic", entry.Name(), base)
		}
	}
}
