package plandiff_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// TestDiff_ParseExceedsTimeout_ReturnsExceededLimits proves acceptance
// criterion 5's per-diff timeout: a parse that blocks past Config.ParseTimeout
// classifies as ExceededLimits rather than hanging Diff forever.
func TestDiff_ParseExceedsTimeout_ReturnsExceededLimits(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) }) // let the abandoned parse goroutine finish so the test process can exit cleanly

	parser := &fakeParser{
		fn: func(int, string) ([]plandiff.Resource, error) {
			<-unblock
			return nil, nil
		},
	}

	engine, err := plandiff.NewEngine(plandiff.Config{ParseTimeout: 20 * time.Millisecond}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	if outcome.Kind != plandiff.ExceededLimits {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.ExceededLimits)
	}
}

// TestDiff_UnifiedDiffTruncatedAtConfiguredCeiling proves acceptance
// criterion 5: unified-diff output is truncated at MaxUnifiedBytes, while
// Summary still reflects the true totals (manifestdiff's own documented
// contract, exercised end-to-end through plandiff).
func TestDiff_UnifiedDiffTruncatedAtConfiguredCeiling(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")

	oldBody := ""
	newBody := ""
	for i := 0; i < 200; i++ {
		oldBody += "line = \"old-value-that-is-reasonably-long\"\n"
		newBody += "line = \"new-value-that-is-reasonably-long\"\n"
	}

	parser := sequentialResourceParser(
		[]plandiff.Resource{{Type: "t", Name: "big", Body: oldBody}},
		[]plandiff.Resource{{Type: "t", Name: "big", Body: newBody}},
	)

	engine, err := plandiff.NewEngine(plandiff.Config{MaxUnifiedBytes: 128}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	if outcome.Kind != plandiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.OK)
	}
	if !outcome.Diff.Truncated {
		t.Errorf("Diff.Truncated = false, want true (output exceeds MaxUnifiedBytes=128)")
	}
	if len(outcome.Diff.Unified) > 128 {
		t.Errorf("len(Unified) = %d, want <= 128", len(outcome.Diff.Unified))
	}
	if outcome.Summary.Changed != 1 {
		t.Errorf("Summary.Changed = %d, want 1 (true count, unaffected by truncation)", outcome.Summary.Changed)
	}
}

// TestDiff_ConcurrencyCapOne_SerializesParses proves acceptance criterion 5's
// concurrency cap: with ConcurrencyCap=1, two concurrent Diff calls (for two
// different commits, so they can't share a cache entry or single-flight
// group) never have more than one parse in flight at once.
func TestDiff_ConcurrencyCapOne_SerializesParses(t *testing.T) {
	t.Parallel()

	var inFlight, maxObservedInFlight int32
	gate := make(chan struct{})

	repo := &fakePlanRepo{
		firstParentFn: func(sha string) (string, error) { return "parent-of-" + sha, nil },
	}
	parser := &fakeParser{
		fn: func(_ int, _ string) ([]plandiff.Resource, error) {
			n := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&maxObservedInFlight)
				if n <= old || atomic.CompareAndSwapInt32(&maxObservedInFlight, old, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return nil, nil
		},
	}

	engine, err := plandiff.NewEngine(plandiff.Config{ConcurrencyCap: 1}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	done := make(chan struct{}, 2)
	for _, sha := range []string{"sha-1", "sha-2"} {
		sha := sha
		go func() {
			<-gate
			engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: sha})
			done <- struct{}{}
		}()
	}
	close(gate)
	<-done
	<-done

	if got := atomic.LoadInt32(&maxObservedInFlight); got != 1 {
		t.Errorf("max observed concurrent parses = %d, want 1 (ConcurrencyCap=1 must serialize)", got)
	}
}
