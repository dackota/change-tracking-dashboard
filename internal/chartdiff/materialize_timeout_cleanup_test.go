package chartdiff_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_TimeoutPath_CleansUpOnlyAfterAbandonedRenderFinishes proves the
// HIGH-2 fix: when a render exceeds Config.RenderTimeout, Diff returns
// ExceededLimits immediately, but the abandoned render goroutine keeps
// running against its materialize temp dir until Renderer.Render itself
// returns. The temp dir must not be removed while that goroutine is still
// touching it — cleanup must wait until the goroutine actually finishes.
func TestDiff_TimeoutPath_CleansUpOnlyAfterAbandonedRenderFinishes(t *testing.T) {
	t.Parallel()

	var chartDirCapture string
	touched := make(chan error, 1)

	repo := fixedParentRepo("parent-sha")
	renderer := &fakeRenderer{
		fn: func(callN int, chartDir string, _ map[string]interface{}) (*chartrender.Result, error) {
			if callN == 1 {
				chartDirCapture = chartDir
				// Exceed the configured RenderTimeout so the caller gives up
				// and Diff returns before this goroutine finishes — this is
				// the "abandoned render still running" window HIGH-2 covers.
				time.Sleep(150 * time.Millisecond)
				// Touch chartDir now, well after Diff has already returned:
				// if cleanup ran prematurely (on the caller's timeout return
				// rather than after this goroutine finishes), the dir is
				// already gone and this write fails.
				err := os.WriteFile(filepath.Join(chartDir, "touched-after-timeout.txt"), []byte("x"), 0o600)
				touched <- err
			}
			return &chartrender.Result{}, nil
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{RenderTimeout: 20 * time.Millisecond}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})
	if outcome.Kind != chartdiff.ExceededLimits {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.ExceededLimits)
	}

	select {
	case writeErr := <-touched:
		if writeErr != nil {
			t.Errorf("abandoned render goroutine's write into chartDir after the caller timed out failed: %v — the temp dir was removed out from under a render that was still touching it", writeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned render goroutine never finished touching chartDir within 2s")
	}

	// The goroutine has now finished touching chartDir; cleanup should
	// follow shortly (asynchronously) — poll briefly rather than asserting
	// an exact instant.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(chartDirCapture); os.IsNotExist(statErr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("chartDir %q still exists after the abandoned render finished, want it cleaned up", chartDirCapture)
}
