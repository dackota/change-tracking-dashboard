package subtree_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
)

// Cache, single-flight and totality behaviour of the shared engine. These
// were previously asserted only against chartdiff; plandiff ran the same code
// with only an indirect cache-determinism property and no eviction,
// concurrency-cap or single-flight coverage at all.

// TestDiff_CachesOutcome_ProducesAtMostOncePerKey proves a repeated request
// for the same key is served from the cache — the domain runs twice (old +
// new) on the first call and never again.
func TestDiff_CachesOutcome_ProducesAtMostOncePerKey(t *testing.T) {
	t.Parallel()

	domain := &fakeDomain{}
	engine := newTestEngine(t, testConfig(), domain)
	repo := fixedParentRepo("parent-sha")

	for i := 0; i < 5; i++ {
		if got := engine.Diff(context.Background(), repo, defaultRequest); got.Kind != kindOK {
			t.Fatalf("call %d: Kind = %q, want %q", i, got.Kind, kindOK)
		}
	}

	if n := domain.callCount(); n != 2 {
		t.Errorf("Produce ran %d times across 5 identical Diffs, want 2 (old + new, then cached)", n)
	}
}

// TestDiff_CachesFailures_NeverRetriesAKnownBadComputation proves the cache
// stores classified failures too, so a known-bad computation is not retried
// on every request — the property that keeps a pathological input from being
// re-rendered indefinitely.
func TestDiff_CachesFailures_NeverRetriesAKnownBadComputation(t *testing.T) {
	t.Parallel()

	domain := &fakeDomain{fn: func(_ int, _ string) ([]string, error) { return nil, errUnexpected }}
	engine := newTestEngine(t, testConfig(), domain)
	repo := fixedParentRepo("parent-sha")

	for i := 0; i < 4; i++ {
		if got := engine.Diff(context.Background(), repo, defaultRequest); got.Kind != kindFailed {
			t.Fatalf("call %d: Kind = %q, want %q", i, got.Kind, kindFailed)
		}
	}

	if n := domain.callCount(); n != 1 {
		t.Errorf("Produce ran %d times across 4 identical failing Diffs, want 1 (failure cached)", n)
	}
}

// TestDiff_NoPriorVersion_IsNotCached proves a root commit is classified
// before a cache key exists, so it never consumes a cache slot — recomputing
// "does this commit have a parent" is one cheap git lookup.
func TestDiff_NoPriorVersion_IsNotCached(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{firstParentFn: func(string) (string, error) { return "", gitsource.ErrNoParent }}
	domain := &fakeDomain{}
	engine := newTestEngine(t, testConfig(), domain)

	for i := 0; i < 3; i++ {
		if got := engine.Diff(context.Background(), repo, defaultRequest); got.Kind != kindNoPriorVersion {
			t.Fatalf("call %d: Kind = %q, want %q", i, got.Kind, kindNoPriorVersion)
		}
	}
	if n := domain.callCount(); n != 0 {
		t.Errorf("Produce ran %d times for a root commit, want 0", n)
	}
}

// TestDiff_CacheEviction_RecomputesEvictedKeys proves CacheEntries genuinely
// bounds the cache: with room for one entry, cycling through two keys evicts
// and recomputes rather than growing without limit.
func TestDiff_CacheEviction_RecomputesEvictedKeys(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.CacheEntries = 1
	domain := &fakeDomain{}
	engine := newTestEngine(t, cfg, domain)
	repo := fixedParentRepo("parent-sha")

	a := subtree.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-a"}
	b := subtree.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-b"}

	engine.Diff(context.Background(), repo, a) // 2 produces
	engine.Diff(context.Background(), repo, b) // 2 produces, evicts a
	engine.Diff(context.Background(), repo, a) // 2 produces again

	if n := domain.callCount(); n != 6 {
		t.Errorf("Produce ran %d times, want 6 — a one-entry cache must evict, not retain both keys", n)
	}
}

// TestDiff_SingleFlight_CoalescesConcurrentIdenticalRequests proves a
// concurrent burst for one key collapses onto a single computation, not just
// that the already-cached fast path works. This is what keeps a thundering
// herd from launching N simultaneous renders.
func TestDiff_SingleFlight_CoalescesConcurrentIdenticalRequests(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	domain := &fakeDomain{fn: func(callN int, _ string) ([]string, error) {
		if callN == 1 {
			<-release // hold the leader until every follower has queued
		}
		return nil, nil
	}}
	engine := newTestEngine(t, testConfig(), domain)
	repo := fixedParentRepo("parent-sha")

	const callers = 8
	var wg sync.WaitGroup
	results := make([]outcome, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = engine.Diff(context.Background(), repo, defaultRequest)
		}(i)
	}

	waitFor(t, func() bool { return domain.callCount() >= 1 })
	close(release)
	wg.Wait()

	if n := domain.callCount(); n != 2 {
		t.Errorf("Produce ran %d times for %d concurrent identical requests, want 2 (old + new, coalesced)", n, callers)
	}
	for i, got := range results {
		if got.Kind != kindOK {
			t.Errorf("caller %d got Kind %q, want %q — every follower must share the leader's outcome", i, got.Kind, kindOK)
		}
	}
}

// TestDiff_ConcurrencyCap_BoundsSimultaneousProduces proves ConcurrencyCap is
// enforced: with a cap of one, no two Produce calls ever overlap.
func TestDiff_ConcurrencyCap_BoundsSimultaneousProduces(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.ConcurrencyCap = 1

	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	domain := &fakeDomain{fn: func(_ int, _ string) ([]string, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
		return nil, nil
	}}

	engine := newTestEngine(t, cfg, domain)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			engine.Diff(context.Background(), fixedParentRepo("parent-sha"),
				subtree.Request{RepoName: "r", TenantPath: "tenant", CommitSha: fmt.Sprintf("sha-%d", i)})
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > 1 {
		t.Errorf("observed %d concurrent Produce calls, want at most 1 (ConcurrencyCap)", maxInFlight)
	}
}

// TestDiff_IsTotal_AlwaysReturnsOneKindAndNeverPanics sweeps every failure
// path the engine can reach and asserts Diff is total: exactly one classified
// Outcome, no panic, for any combination of repo and domain behaviour.
func TestDiff_IsTotal_AlwaysReturnsOneKindAndNeverPanics(t *testing.T) {
	t.Parallel()

	// A panicking FirstParent is deliberately absent: it is called
	// synchronously on the caller's goroutine and is not recovered, so it is
	// the one input for which Diff is not total. See
	// TestDiff_FirstParentPanic_IsNotContained.
	repos := map[string]func(string) (string, error){
		"ok":       func(string) (string, error) { return "parent-sha", nil },
		"no-paren": func(string) (string, error) { return "", gitsource.ErrNoParent },
		"error":    func(string) (string, error) { return "", errUnexpected },
	}
	materializers := map[string]func(_, _, _ string, _ gitsource.MaterializeBounds) error{
		"ok":     func(_, _, _ string, _ gitsource.MaterializeBounds) error { return nil },
		"bounds": func(_, _, _ string, _ gitsource.MaterializeBounds) error { return gitsource.ErrMaterializeBoundsExceeded },
		"error":  func(_, _, _ string, _ gitsource.MaterializeBounds) error { return errUnexpected },
		"panic":  func(_, _, _ string, _ gitsource.MaterializeBounds) error { panic("materialize exploded") },
	}
	produces := map[string]func(int, string) ([]string, error){
		"ok":    func(int, string) ([]string, error) { return nil, nil },
		"error": func(int, string) ([]string, error) { return nil, errUnexpected },
		"panic": func(int, string) ([]string, error) { panic("produce exploded") },
	}

	valid := map[string]bool{kindOK: true, kindNoPriorVersion: true, kindExceeded: true, kindFailed: true}

	for rn, rf := range repos {
		for mn, mf := range materializers {
			for pn, pf := range produces {
				name := rn + "/" + mn + "/" + pn
				t.Run(name, func(t *testing.T) {
					engine := newTestEngine(t, testConfig(), &fakeDomain{fn: pf})
					got := engine.Diff(context.Background(), &fakeRepo{firstParentFn: rf, materializeFn: mf}, defaultRequest)
					if !valid[got.Kind] {
						t.Errorf("Kind = %q, want one of the domain's classified kinds", got.Kind)
					}
				})
			}
		}
	}
}

// TestDiff_FirstParentPanic_IsNotContained documents a real limitation: the
// engine recovers panics raised inside the materialize and produce
// goroutines, but Repo.FirstParent is called synchronously on the caller's
// own goroutine and is NOT guarded. A panicking FirstParent propagates.
//
// *gitsource.Source.FirstParent does not walk untrusted tree content the way
// MaterializeSubtreeBounded does, so this is accepted rather than closed —
// but it is asserted here so the gap is a decision on record, not a surprise.
func TestDiff_FirstParentPanic_IsNotContained(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t, testConfig(), &fakeDomain{})
	repo := &fakeRepo{firstParentFn: func(string) (string, error) { panic("first parent exploded") }}

	defer func() {
		if r := recover(); r == nil {
			t.Error("FirstParent panic was contained — if that is now intended, update this test and the engine's doc")
		}
	}()
	engine.Diff(context.Background(), repo, defaultRequest)
}
