package subtree_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
)

// These are the temp-dir cleanup-lifecycle and panic-containment invariants
// materializeAndProduce's doc claims. They were previously asserted only
// against chartdiff, even though plandiff ran a byte-for-byte copy of the
// same code with none of these tests. Asserting them here covers every domain
// on this engine at once.

// TestDiff_MaterializesIntoExclusiveTempDirs proves each side is materialized
// into its own freshly created directory (never a shared or caller-supplied
// path), the directory is caller-exclusive (mode 0700), and it is removed
// once Diff returns — nothing lingers on disk past the call.
func TestDiff_MaterializesIntoExclusiveTempDirs(t *testing.T) {
	t.Parallel()

	domain := &fakeDomain{fn: func(_ int, dir string) ([]string, error) {
		if runtime.GOOS != "windows" {
			info, err := os.Stat(dir)
			if err != nil {
				t.Errorf("stat %q: %v", dir, err)
				return nil, err
			}
			if perm := info.Mode().Perm(); perm&0o077 != 0 {
				t.Errorf("materialize dir %q has mode %o, want no group/other permission bits set", dir, perm)
			}
		}
		return nil, nil
	}}

	engine := newTestEngine(t, testConfig(), domain)
	engine.Diff(context.Background(), fixedParentRepo("parent-sha"), defaultRequest)

	dirs := domain.observedDirs()
	if len(dirs) != 2 {
		t.Fatalf("Produce saw %d dirs, want 2 (old + new)", len(dirs))
	}
	if dirs[0] == dirs[1] {
		t.Errorf("old and new sides materialized into the same dir %q, want distinct exclusive dirs", dirs[0])
	}
	for _, dir := range dirs {
		if !strings.Contains(filepath.Base(dir), "testdiff-") {
			t.Errorf("temp dir %q does not carry the configured Labels.Name prefix", dir)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("materialize dir %q still exists after Diff returned, want it cleaned up (stat err: %v)", dir, err)
		}
	}
}

// TestDiff_MaterializePanic_IsContainedAndCleansUp proves a panic inside
// Repo.MaterializeSubtreeBounded — in threat model, since it walks untrusted
// repository content — is recovered on the goroutine that raised it, folded
// into a classified Outcome, and still cleans up the temp directory.
func TestDiff_MaterializePanic_IsContainedAndCleansUp(t *testing.T) {
	t.Parallel()

	var destDirs []string
	var mu sync.Mutex
	repo := &fakeRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(_, _, destDir string, _ gitsource.MaterializeBounds) error {
			mu.Lock()
			destDirs = append(destDirs, destDir)
			mu.Unlock()
			panic("materialize exploded")
		},
	}

	engine := newTestEngine(t, testConfig(), &fakeDomain{})

	got := engine.Diff(context.Background(), repo, defaultRequest)

	if got.Kind != kindFailed {
		t.Errorf("Kind = %q, want %q — a materialize panic must be contained, not propagated", got.Kind, kindFailed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(destDirs) == 0 {
		t.Fatal("materialize was never called")
	}
	for _, dir := range destDirs {
		waitGone(t, dir)
	}
}

// TestDiff_ProducePanic_IsContainedAndCleansUp proves the same for a panic
// raised by the domain's own Produce — a hostile input reaching the Helm SDK
// or the HCL parser must not take the process down, and must not leak the
// temp directory.
func TestDiff_ProducePanic_IsContainedAndCleansUp(t *testing.T) {
	t.Parallel()

	domain := &fakeDomain{fn: func(_ int, _ string) ([]string, error) {
		panic("produce exploded")
	}}
	engine := newTestEngine(t, testConfig(), domain)

	got := engine.Diff(context.Background(), fixedParentRepo("parent-sha"), defaultRequest)

	if got.Kind != kindFailed {
		t.Errorf("Kind = %q, want %q — a produce panic must be contained", got.Kind, kindFailed)
	}
	if got.Cause == nil || !strings.Contains(got.Cause.Error(), "produce panicked") {
		t.Errorf("Cause = %v, want the recovered panic surfaced to the domain as a produce error", got.Cause)
	}
	for _, dir := range domain.observedDirs() {
		waitGone(t, dir)
	}
}

// TestDiff_MaterializeTimeout_CleansUpOnlyAfterAbandonedCallFinishes is the
// ordering invariant materializeBounded's doc turns on: on timeout the engine
// returns immediately, but the abandoned goroutine is still writing to
// destDir, so cleanup must wait for it. Removing the directory early would be
// a use-after-free-class race.
func TestDiff_MaterializeTimeout_CleansUpOnlyAfterAbandonedCallFinishes(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	stillPresent := make(chan bool, 1)
	var destDir string

	repo := &fakeRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(_, _, dir string, _ gitsource.MaterializeBounds) error {
			destDir = dir
			<-release // outlive the timeout
			// Still "touching" destDir here: it must not have been removed.
			_, err := os.Stat(dir)
			stillPresent <- err == nil
			return nil
		},
	}

	cfg := testConfig()
	cfg.MaterializeTimeout = 30 * time.Millisecond
	engine := newTestEngine(t, cfg, &fakeDomain{})

	got := engine.Diff(context.Background(), repo, defaultRequest)
	if got.Kind != kindExceeded {
		t.Errorf("Kind = %q, want %q on materialize timeout", got.Kind, kindExceeded)
	}

	close(release)
	if present := <-stillPresent; !present {
		t.Error("destDir was removed while the abandoned materialize was still running — the cleanup handoff is broken")
	}
	waitGone(t, destDir)
}

// TestDiff_ProduceTimeout_CleansUpOnlyAfterAbandonedCallFinishes is the same
// ordering invariant for produceBounded's half of the handoff.
func TestDiff_ProduceTimeout_CleansUpOnlyAfterAbandonedCallFinishes(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	stillPresent := make(chan bool, 1)
	var produceDir string

	domain := &fakeDomain{fn: func(_ int, dir string) ([]string, error) {
		produceDir = dir
		<-release
		_, err := os.Stat(dir)
		stillPresent <- err == nil
		return nil, nil
	}}

	cfg := testConfig()
	cfg.ProduceTimeout = 30 * time.Millisecond
	engine := newTestEngine(t, cfg, domain)

	got := engine.Diff(context.Background(), fixedParentRepo("parent-sha"), defaultRequest)
	if got.Kind != kindExceeded {
		t.Errorf("Kind = %q, want %q on produce timeout", got.Kind, kindExceeded)
	}

	close(release)
	if present := <-stillPresent; !present {
		t.Error("dir was removed while the abandoned produce was still running — the cleanup handoff is broken")
	}
	waitGone(t, produceDir)
}

// TestDiff_CtxCancelledWhileQueuedForSlot_CleansUpWithoutRunning proves the
// slot acquire itself respects ctx: with every materialize slot busy, a
// caller whose ctx is already cancelled must not block, must never reach the
// repo, and must still clean up the directory it had already created.
func TestDiff_CtxCancelledWhileQueuedForSlot_CleansUpWithoutRunning(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	defer close(block)

	occupy := &fakeRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(_, _, _ string, _ gitsource.MaterializeBounds) error {
			<-block
			return nil
		},
	}

	cfg := testConfig()
	cfg.MaterializeConcurrencyCap = 1
	engine := newTestEngine(t, cfg, &fakeDomain{})

	// Saturate the single materialize slot.
	go engine.Diff(context.Background(), occupy, subtree.Request{RepoName: "r", TenantPath: "t", CommitSha: "occupier"})
	waitFor(t, func() bool { return occupy.materializeCallCount() == 1 })

	blocked := &fakeRepo{firstParentFn: func(string) (string, error) { return "parent-sha", nil }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan outcome, 1)
	go func() { done <- engine.Diff(ctx, blocked, defaultRequest) }()

	select {
	case got := <-done:
		if got.Kind != kindExceeded {
			t.Errorf("Kind = %q, want %q for a ctx cancelled while queued", got.Kind, kindExceeded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Diff blocked on a saturated semaphore despite a cancelled ctx")
	}

	if n := blocked.materializeCallCount(); n != 0 {
		t.Errorf("materialize ran %d times for a cancelled ctx, want 0 — the slot acquire must lose the race to ctx.Done()", n)
	}
}

// TestDiff_BoundsExceeded_ClassifiesWithoutLogging proves the engine
// recognises gitsource.ErrMaterializeBoundsExceeded and hands the domain
// ExceededLimits rather than the generic bucket.
func TestDiff_BoundsExceeded_ClassifiesAsExceededLimits(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(_, _, _ string, _ gitsource.MaterializeBounds) error {
			return gitsource.ErrMaterializeBoundsExceeded
		},
	}

	engine := newTestEngine(t, testConfig(), &fakeDomain{})
	if got := engine.Diff(context.Background(), repo, defaultRequest); got.Kind != kindExceeded {
		t.Errorf("Kind = %q, want %q", got.Kind, kindExceeded)
	}
}

// TestDiff_ThreadsConfiguredMaterializeBoundsToRepo proves every configured
// ceiling actually reaches Repo.MaterializeSubtreeBounded — including
// MaxTreeNodes, the one that closes the "thousands of empty directories" gap
// neither the byte nor the file ceiling catches.
func TestDiff_ThreadsConfiguredMaterializeBoundsToRepo(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.Materialize = gitsource.MaterializeBounds{
		MaxTotalBytes: 111,
		MaxFiles:      222,
		MaxDepth:      333,
		MaxTreeNodes:  444,
	}

	repo := fixedParentRepo("parent-sha")
	engine := newTestEngine(t, cfg, &fakeDomain{})
	engine.Diff(context.Background(), repo, defaultRequest)

	if got := repo.boundsSeen(); got != cfg.Materialize {
		t.Errorf("bounds reaching the repo = %+v, want %+v", got, cfg.Materialize)
	}
}

// TestDiff_ProduceError_ReachesDomainUnwrapped proves the engine hands the
// domain's own error back verbatim, which is what lets each domain classify
// its private error vocabulary (chartrender.Failure, errBlockDepthExceeded)
// without the engine knowing either exists.
func TestDiff_ProduceError_ReachesDomainUnwrapped(t *testing.T) {
	t.Parallel()

	domain := &fakeDomain{fn: func(_ int, _ string) ([]string, error) { return nil, errUnexpected }}
	engine := newTestEngine(t, testConfig(), domain)

	got := engine.Diff(context.Background(), fixedParentRepo("parent-sha"), defaultRequest)

	if !errors.Is(got.Cause, errUnexpected) {
		t.Errorf("Cause = %v, want the domain's own error unwrapped", got.Cause)
	}
}

// waitGone fails the test unless dir disappears within a short window.
// Cleanup on the abandoned-goroutine paths is deliberately asynchronous, so
// this polls rather than asserting immediately.
func waitGone(t *testing.T, dir string) {
	t.Helper()
	if dir == "" {
		return
	}
	waitFor(t, func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	})
}

// waitFor polls cond for up to two seconds, failing the test if it never
// holds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never held within 2s")
}
