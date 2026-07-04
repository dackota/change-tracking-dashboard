package chartdiff_test

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_MaterializesIntoExclusiveTempDirs proves security control 1: each
// side is materialized into its own freshly created directory (never a
// shared or caller-supplied path), the directory is caller-exclusive
// (mode 0700), and it is removed once Diff returns — nothing lingers on disk
// past the call.
func TestDiff_MaterializesIntoExclusiveTempDirs(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	var observedDirs []string
	renderer := &fakeRenderer{
		fn: func(_ int, chartDir string, _ map[string]interface{}) (*chartrender.Result, error) {
			observedDirs = append(observedDirs, chartDir)

			if runtime.GOOS != "windows" {
				info, err := os.Stat(chartDir)
				if err != nil {
					t.Fatalf("stat %q: %v", chartDir, err)
				}
				if perm := info.Mode().Perm(); perm&0o077 != 0 {
					t.Errorf("materialize dir %q has mode %o, want no group/other permission bits set", chartDir, perm)
				}
			}
			return &chartrender.Result{}, nil
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

	if len(observedDirs) != 2 {
		t.Fatalf("renderer observed %d chart dirs, want 2 (old + new)", len(observedDirs))
	}
	if observedDirs[0] == observedDirs[1] {
		t.Errorf("old and new sides materialized into the same dir %q, want distinct exclusive dirs", observedDirs[0])
	}
	for _, dir := range observedDirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("materialize dir %q still exists after Diff returned, want it cleaned up (stat err: %v)", dir, err)
		}
	}
}
