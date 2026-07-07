package chartdiff_test

import (
	"context"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
)

// TestDiff_CacheEntriesOne_EvictsOlderEntry proves the LRU eviction Config
// wires through: with CacheEntries=1, adding a second distinct key evicts
// the first, so a repeat request for the now-evicted key is a genuine miss
// (the renderer is invoked again for it) rather than an unbounded cache.
func TestDiff_CacheEntriesOne_EvictsOlderEntry(t *testing.T) {
	t.Parallel()

	repo := &fakeChartRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{CacheEntries: 1}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	req1 := chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-1"}
	req2 := chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-2"}

	engine.Diff(context.Background(), repo, req1) // 2 renders (old+new), occupies the single cache slot
	if got := renderer.callCount(); got != 2 {
		t.Fatalf("after req1, renderer invoked %d times, want 2", got)
	}

	engine.Diff(context.Background(), repo, req2) // a distinct key: evicts req1's entry under CacheEntries=1
	if got := renderer.callCount(); got != 4 {
		t.Fatalf("after req2, renderer invoked %d times, want 4 (2 more renders)", got)
	}

	// req1's entry was evicted to make room for req2 — repeating it must be
	// a genuine miss (the renderer is invoked again), not still cached.
	engine.Diff(context.Background(), repo, req1)
	if got := renderer.callCount(); got != 6 {
		t.Errorf("after repeating req1 post-eviction, renderer invoked %d times, want 6 (2 more renders — a real re-render, not a stale cache hit)", got)
	}
}
