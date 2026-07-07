package chartdiff_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
)

// TestDiff_MaterializeTimeoutPath_CleansUpOnlyAfterAbandonedMaterializeFinishes
// is materialize's counterpart to
// TestDiff_TimeoutPath_CleansUpOnlyAfterAbandonedRenderFinishes
// (materialize_timeout_cleanup_test.go): when
// ChartRepo.MaterializeSubtreeBounded exceeds Config.MaterializeTimeout, Diff
// must return ExceededLimits immediately, but the abandoned materialize
// goroutine keeps running against its temp dir until the call itself
// returns — the temp dir must not be removed while that goroutine is still
// touching it.
func TestDiff_MaterializeTimeoutPath_CleansUpOnlyAfterAbandonedMaterializeFinishes(t *testing.T) {
	t.Parallel()

	var destDirCapture string
	touched := make(chan error, 1)

	repo := &fakeChartRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(sha, _, destDir string, _ gitsource.MaterializeBounds) error {
			if sha == "parent-sha" {
				destDirCapture = destDir
				// Exceed the configured MaterializeTimeout so the caller
				// gives up and Diff returns before this goroutine finishes.
				time.Sleep(150 * time.Millisecond)
				// Touch destDir well after Diff has already returned: if
				// cleanup ran prematurely, the dir is already gone and this
				// write fails.
				err := os.WriteFile(filepath.Join(destDir, "touched-after-timeout.txt"), []byte("x"), 0o600)
				touched <- err
			}
			return nil
		},
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{MaterializeTimeout: 20 * time.Millisecond}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "commit-sha"})
	if outcome.Kind != chartdiff.ExceededLimits {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.ExceededLimits)
	}
	if renderer.callCount() != 0 {
		t.Errorf("renderer invoked %d times, want 0 (materialize never completed on the timed-out side)", renderer.callCount())
	}

	select {
	case writeErr := <-touched:
		if writeErr != nil {
			t.Errorf("abandoned materialize goroutine's write into destDir after the caller timed out failed: %v — the temp dir was removed out from under a materialize call that was still touching it", writeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned materialize goroutine never finished touching destDir within 2s")
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(destDirCapture); os.IsNotExist(statErr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("destDir %q still exists after the abandoned materialize call finished, want it cleaned up", destDirCapture)
}

// TestDiff_MaterializeContextCancelledWhileQueuedForSlot_ReturnsPromptly is
// materialize's counterpart to
// TestDiff_ContextCancelledWhileQueuedForSlot_ReturnsPromptly
// (acquire_ctx_test.go): with every MaterializeConcurrencyCap slot busy, a
// caller whose ctx is already cancelled while queued for a materialize slot
// must not block on the semaphore acquire — it must notice ctx.Done() and
// return ExceededLimits promptly.
func TestDiff_MaterializeContextCancelledWhileQueuedForSlot_ReturnsPromptly(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	blockForever := make(chan struct{})
	t.Cleanup(func() { close(blockForever) })

	var startedOnce sync.Once
	repo := &fakeChartRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
		materializeFn: func(string, string, string, gitsource.MaterializeBounds) error {
			startedOnce.Do(func() { close(started) })
			<-blockForever
			return nil
		},
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{MaterializeConcurrencyCap: 1}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Occupy the single materialize concurrency slot indefinitely with a
	// first, distinct request.
	go engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-1"})
	<-started

	// A second, distinct request queued with an already-cancelled ctx: with
	// the slot unavailable, this must return via ctx.Done(), not block on
	// the semaphore send.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resultCh := make(chan chartdiff.Outcome, 1)
	start := time.Now()
	go func() {
		resultCh <- engine.Diff(ctx, repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-2"})
	}()

	select {
	case outcome := <-resultCh:
		elapsed := time.Since(start)
		if outcome.Kind != chartdiff.ExceededLimits {
			t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.ExceededLimits)
		}
		if elapsed > time.Second {
			t.Errorf("Diff took %v to return with an already-cancelled ctx and no free materialize slot, want it to return promptly", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Diff with an already-cancelled ctx did not return within 2s while all materialize concurrency slots were busy — it blocked on the semaphore acquire instead of noticing ctx.Done()")
	}
}

// TestDiff_MaterializeConcurrencyCapOne_SerializesMaterializations proves
// materialize has its own concurrency gate, independent of render: with
// MaterializeConcurrencyCap=1, two concurrent Diff calls for two different
// commits never have more than one materialize call in flight at once.
func TestDiff_MaterializeConcurrencyCapOne_SerializesMaterializations(t *testing.T) {
	t.Parallel()

	var inFlight int32
	var maxObservedInFlight int32
	gate := make(chan struct{})

	repo := &fakeChartRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
		materializeFn: func(string, string, string, gitsource.MaterializeBounds) error {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&maxObservedInFlight)
				if n <= old || atomic.CompareAndSwapInt32(&maxObservedInFlight, old, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return nil
		},
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{MaterializeConcurrencyCap: 1, ConcurrencyCap: 4}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		sha := []string{"sha-1", "sha-2"}[i]
		go func() {
			defer wg.Done()
			<-gate
			engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: sha})
		}()
	}
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&maxObservedInFlight); got != 1 {
		t.Errorf("max observed concurrent materializations = %d, want 1 (MaterializeConcurrencyCap=1 must serialize)", got)
	}
}

// TestDiff_MaterializeConcurrencyCapTwo_AllowsTwoSimultaneousMaterializations
// is the converse check: raising the cap to 2 lets two materializations
// overlap, proving the serialization above is really caused by the
// materialize-specific cap.
func TestDiff_MaterializeConcurrencyCapTwo_AllowsTwoSimultaneousMaterializations(t *testing.T) {
	t.Parallel()

	var inFlight int32
	var maxObservedInFlight int32
	gate := make(chan struct{})

	repo := &fakeChartRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
		materializeFn: func(string, string, string, gitsource.MaterializeBounds) error {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&maxObservedInFlight)
				if n <= old || atomic.CompareAndSwapInt32(&maxObservedInFlight, old, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return nil
		},
	}
	renderer := &fakeRenderer{}

	engine, err := chartdiff.NewEngine(chartdiff.Config{MaterializeConcurrencyCap: 2, ConcurrencyCap: 4}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		sha := []string{"sha-1", "sha-2"}[i]
		go func() {
			defer wg.Done()
			<-gate
			engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: sha})
		}()
	}
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&maxObservedInFlight); got < 2 {
		t.Errorf("max observed concurrent materializations = %d, want >= 2 (MaterializeConcurrencyCap=2 should allow overlap)", got)
	}
}

// TestDiff_ConcurrentSameKeyCalls_MaterializesAtMostOncePerSide_Property is
// the class-level invariant test for the round-1 bug (cacheKey/singleflight
// collision + concurrency): firing a burst of concurrent Diff calls for the
// SAME key must still materialize each side (old, new) at most once — the
// new materialize-side concurrency gate must not reintroduce the collision
// the existing singleflight group already closed for render. Run with
// -race, this also proves the materialize semaphore and the cache/
// single-flight group are correctly synchronized under real concurrent
// access.
func TestDiff_ConcurrentSameKeyCalls_MaterializesAtMostOncePerSide_Property(t *testing.T) {
	t.Parallel()

	const callers = 50

	repo := &fakeChartRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(string, string, string, gitsource.MaterializeBounds) error {
			time.Sleep(5 * time.Millisecond) // widen the race window
			return nil
		},
	}
	renderer := &fakeRenderer{
		fn: func(int, string, map[string]interface{}) (*chartrender.Result, error) {
			return &chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: "app", YAML: "v: 1\n"}}}, nil
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{ConcurrencyCap: callers, MaterializeConcurrencyCap: callers}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	req := chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"}

	outcomes := make([]chartdiff.Outcome, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		i := i
		go func() {
			defer wg.Done()
			outcomes[i] = engine.Diff(context.Background(), repo, req)
		}()
	}
	wg.Wait()

	for i, got := range outcomes {
		if got != outcomes[0] {
			t.Errorf("outcomes[%d] = %+v, want identical to outcomes[0] = %+v", i, got, outcomes[0])
		}
	}

	if got := repo.materializeCallCount(); got != 2 {
		t.Errorf("MaterializeSubtreeBounded invoked %d times across %d concurrent callers for the same key, want exactly 2 (old + new, once each)", got, callers)
	}
	if got := renderer.callCount(); got != 2 {
		t.Errorf("renderer invoked %d times across %d concurrent callers for the same key, want exactly 2 (old + new, once each)", got, callers)
	}
}
