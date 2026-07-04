package chartdiff_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_ConcurrencyCapOne_SerializesRenders proves acceptance criterion 5:
// with ConcurrencyCap=1, two concurrent Diff calls (for two different commits,
// so they can't share a cache entry or single-flight group) never have more
// than one render in flight at once — the second can't enter render until
// the first releases.
func TestDiff_ConcurrencyCapOne_SerializesRenders(t *testing.T) {
	t.Parallel()

	var inFlight int32
	var maxObservedInFlight int32
	gate := make(chan struct{}) // closed once both calls have started, to prove they actually overlapped in time

	repo := &fakeChartRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
	}
	renderer := &fakeRenderer{
		fn: func(_ int, _ string, _ map[string]interface{}) (*chartrender.Result, error) {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&maxObservedInFlight)
				if n <= old || atomic.CompareAndSwapInt32(&maxObservedInFlight, old, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return &chartrender.Result{}, nil
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{ConcurrencyCap: 1}, renderer)
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
		t.Errorf("max observed concurrent renders = %d, want 1 (ConcurrencyCap=1 must serialize)", got)
	}
}

// TestDiff_ConcurrencyCapTwo_AllowsTwoSimultaneousRenders is the converse
// check: raising the cap to 2 does let two renders overlap, proving the
// serialization above is actually caused by the cap (not, say, an accidental
// global lock).
func TestDiff_ConcurrencyCapTwo_AllowsTwoSimultaneousRenders(t *testing.T) {
	t.Parallel()

	var inFlight int32
	var maxObservedInFlight int32
	gate := make(chan struct{})

	repo := &fakeChartRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
	}
	renderer := &fakeRenderer{
		fn: func(_ int, _ string, _ map[string]interface{}) (*chartrender.Result, error) {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&maxObservedInFlight)
				if n <= old || atomic.CompareAndSwapInt32(&maxObservedInFlight, old, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return &chartrender.Result{}, nil
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{ConcurrencyCap: 2}, renderer)
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
		t.Errorf("max observed concurrent renders = %d, want >= 2 (ConcurrencyCap=2 should allow overlap)", got)
	}
}
