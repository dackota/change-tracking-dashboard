package chartdiff_test

import (
	"context"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
)

// TestDiff_ContextCancelledWhileQueuedForSlot_ReturnsPromptly proves the
// HIGH-1 fix: when every ConcurrencyCap slot is busy, a caller whose ctx is
// already cancelled (or expires) while queued for a slot must not block on
// the semaphore acquire — it must notice the cancellation and return
// ExceededLimits promptly, the same as a render that times out after it
// starts.
func TestDiff_ContextCancelledWhileQueuedForSlot_ReturnsPromptly(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	blockForever := make(chan struct{})
	t.Cleanup(func() { close(blockForever) }) // let the held slot's render finish so nothing leaks past the test

	repo := &fakeChartRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
	}
	renderer := &fakeRenderer{
		fn: func(callN int, _ string, _ map[string]interface{}) (*chartrender.Result, error) {
			if callN == 1 {
				close(started)
				<-blockForever
			}
			return &chartrender.Result{}, nil
		},
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{ConcurrencyCap: 1}, renderer)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Occupy the single concurrency slot indefinitely (until t.Cleanup
	// unblocks it) with a first, distinct request.
	go engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha-1"})
	<-started // wait until the first render has actually acquired the slot

	// A second, distinct request (so it can't share a cache entry or
	// single-flight group with the first) queued with an already-cancelled
	// ctx: with the slot unavailable, this must return via ctx.Done(), not
	// block on the semaphore send.
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
			t.Errorf("Diff took %v to return with an already-cancelled ctx and no free slot, want it to return promptly", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Diff with an already-cancelled ctx did not return within 2s while all concurrency slots were busy — it blocked on the semaphore acquire instead of noticing ctx.Done()")
	}
}
