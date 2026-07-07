package chartdiff_test

import (
	"context"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_CacheHit_DoesNotReinvokeRenderer proves acceptance criterion 3: a
// second Diff call for the same (repo, tenantPath, parentSha, commitSha) key
// is served from cache without touching the renderer again.
func TestDiff_CacheHit_DoesNotReinvokeRenderer(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	renderer := sequentialManifestRenderer(
		&chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: "app", YAML: "v: 1\n"}}},
		&chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: "app", YAML: "v: 2\n"}}},
	)

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	req := chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"}

	first := engine.Diff(context.Background(), repo, req)
	if first.Kind != chartdiff.OK {
		t.Fatalf("first call outcome.Kind = %q, want %q", first.Kind, chartdiff.OK)
	}
	if got := renderer.callCount(); got != 2 {
		t.Fatalf("after first call, renderer invoked %d times, want 2 (old + new)", got)
	}

	second := engine.Diff(context.Background(), repo, req)
	if second != first {
		t.Errorf("second call outcome = %+v, want identical to first = %+v", second, first)
	}
	if got := renderer.callCount(); got != 2 {
		t.Errorf("after second (cached) call, renderer invoked %d times, want still 2", got)
	}
}

// TestDiff_KnownUnavailable_IsCached proves acceptance criterion 3's other
// half: a classified failure (Unavailable here) is cached too, so a
// known-bad render is never re-attempted.
func TestDiff_KnownUnavailable_IsCached(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	renderer := &fakeRenderer{
		fn: func(int, string, map[string]interface{}) (*chartrender.Result, error) {
			return nil, &chartrender.Failure{Reason: chartrender.ReasonDependencyNotVendored}
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	req := chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"}

	first := engine.Diff(context.Background(), repo, req)
	if first.Kind != chartdiff.Unavailable {
		t.Fatalf("first call outcome.Kind = %q, want %q", first.Kind, chartdiff.Unavailable)
	}
	callsAfterFirst := renderer.callCount()
	if callsAfterFirst != 1 {
		t.Fatalf("after first call, renderer invoked %d times, want 1", callsAfterFirst)
	}

	second := engine.Diff(context.Background(), repo, req)
	if second.Kind != chartdiff.Unavailable {
		t.Errorf("second call outcome.Kind = %q, want %q", second.Kind, chartdiff.Unavailable)
	}
	if got := renderer.callCount(); got != callsAfterFirst {
		t.Errorf("after second (cached) call, renderer invoked %d times, want still %d", got, callsAfterFirst)
	}
}

// TestDiff_DifferentCommits_AreDistinctCacheEntries proves the cache key is
// scoped correctly: two different commit SHAs (with the same repo/tenant)
// each trigger their own render, rather than colliding into one entry.
func TestDiff_DifferentCommits_AreDistinctCacheEntries(t *testing.T) {
	t.Parallel()

	repo := &fakeChartRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-1"})
	engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-2"})

	if got := renderer.callCount(); got != 4 {
		t.Errorf("renderer invoked %d times across two distinct commits, want 4 (2 renders each)", got)
	}
}
