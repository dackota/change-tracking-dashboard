package chartdiff

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/manifestdiff"
)

// materializeAndRender materializes sha's tenant subtree into a fresh,
// caller-exclusive temp directory and renders it. ok is false when a
// terminal classified Outcome was reached (the caller should return it
// immediately); ok is true when manifests holds a successful render.
func (e *Engine) materializeAndRender(ctx context.Context, repo ChartRepo, req Request, sha string, bounds gitsource.MaterializeBounds, side string) (manifests []manifestdiff.Manifest, outcome Outcome, ok bool) {
	destDir, cleanup, err := newExclusiveTempDir()
	if err != nil {
		log.Printf("chartdiff: create temp materialize dir (%s side) repo=%q tenant=%q sha=%q: %v", side, req.RepoName, req.TenantPath, sha, err)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	if err := repo.MaterializeSubtreeBounded(sha, req.TenantPath, destDir, bounds); err != nil {
		// No render goroutine was ever started against destDir — safe to
		// clean up directly, right here.
		cleanup()
		if errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
			return nil, Outcome{Kind: ExceededLimits}, false
		}
		log.Printf("chartdiff: materialize (%s side) repo=%q tenant=%q sha=%q: %v", side, req.RepoName, req.TenantPath, sha, err)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	// From here on, renderBounded owns destDir's cleanup: a render goroutine
	// may be started against it, and destDir must not be removed until that
	// goroutine has actually stopped touching it (see renderBounded's doc).
	result, err, timedOut := e.renderBounded(ctx, destDir, cleanup)
	if timedOut {
		log.Printf("chartdiff: render (%s side) repo=%q tenant=%q sha=%q exceeded timeout %v", side, req.RepoName, req.TenantPath, sha, e.cfg.RenderTimeout)
		return nil, Outcome{Kind: ExceededLimits}, false
	}
	if err != nil {
		var failure *chartrender.Failure
		if errors.As(err, &failure) {
			switch failure.Reason {
			case chartrender.ReasonDependencyNotVendored:
				return nil, Outcome{Kind: Unavailable}, false
			case chartrender.ReasonMalformedChart:
				return nil, Outcome{Kind: CouldNotRender}, false
			}
		}
		log.Printf("chartdiff: render (%s side) repo=%q tenant=%q sha=%q: %v", side, req.RepoName, req.TenantPath, sha, err)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	return toManifestdiffManifests(result), Outcome{}, true
}

// newExclusiveTempDir creates a fresh, unpredictable-named, caller-exclusive
// temp directory (os.MkdirTemp's documented mode is 0700, subject to
// umask — never more permissive) for a single materialize+render. Using a
// freshly created, exclusively-owned directory per render — never a
// caller-supplied or tenant-derived path — closes the TOCTOU/symlink-follow
// risk of a shared or externally-writable destination directory: a
// world-readable file written under a 0700 directory is still unreachable to
// any other user on the host.
func newExclusiveTempDir() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "chartdiff-*")
	if err != nil {
		return "", nil, fmt.Errorf("chartdiff: create temp materialize dir: %w", err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// renderResult carries a render goroutine's outcome back to renderBounded's
// select.
type renderResult struct {
	result *chartrender.Result
	err    error
}

// renderBounded runs a single Renderer.Render call under both the
// concurrency semaphore and the per-render timeout, against the
// caller-exclusive chartDir materializeAndRender created — and owns cleaning
// that directory up via cleanup exactly once, at the right time (see below).
//
// Acquiring the semaphore slot itself respects ctx: under a saturated queue
// (every ConcurrencyCap slot busy), a caller whose ctx is already cancelled
// or expires while queued for a slot must not block on the acquire — the
// select below races the slot send against ctx.Done(), so cancellation while
// still queued is noticed immediately, before ever touching the renderer or
// the timeout timer. No render goroutine was ever started in that case, so
// cleanup runs directly, right there.
//
// Render runs in its own goroutine because chartrender.Render is
// synchronous and cannot be interrupted mid-render (the Helm SDK has no
// cancellation hook). On timeout or ctx cancellation, renderBounded returns
// immediately (timedOut == true) so the caller isn't blocked — but the
// goroutine keeps running against chartDir until Render itself returns, so
// cleanup must not run yet: doing so would remove chartDir out from under a
// render that is still reading it. Because done is a buffered, single-value
// channel, it is received from in exactly one place: either the select below
// (the fast path, once Render finishes before the deadline) or the small
// worker goroutine spawned on the timeout/cancel branch (which blocks on
// <-done until the abandoned render actually finishes, then cleans up) —
// never both. This guarantees cleanup happens exactly once, and only after
// the goroutine has genuinely stopped touching chartDir, without needing a
// sync.Once or shared mutable flag to coordinate the two paths.
//
// The semaphore slot is released only when the goroutine itself finishes
// (the deferred release is inside the goroutine, not after the select), so a
// timed-out render still counts against ConcurrencyCap until it actually
// completes. This is a deliberate, documented trade-off: it keeps the cap
// bounding real concurrent Helm work rather than leaking unbounded
// goroutines, at the cost that a genuinely non-terminating render would
// eventually starve the semaphore. Real Helm templates terminate on finite
// input, so this is accepted as a v1 limitation (ADR 0002).
func (e *Engine) renderBounded(ctx context.Context, chartDir string, cleanup func()) (result *chartrender.Result, err error, timedOut bool) {
	select {
	case e.sem <- struct{}{}: // acquired a concurrency slot
	case <-ctx.Done():
		cleanup() // gave up queued for a slot; never touched the renderer
		return nil, nil, true
	}

	done := make(chan renderResult, 1)
	go func() {
		defer func() { <-e.sem }() // release only once the render itself finishes
		// A hostile or malformed chart could trigger a panic deep in the
		// Helm SDK, well outside this package's control. recover folds that
		// into a plain error — which (not being a *chartrender.Failure)
		// falls through materializeAndRender's classification to the same
		// safe CouldNotRender bucket as any other unclassified render
		// failure — rather than letting an unrecovered goroutine panic take
		// down the whole dashboard process.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("chartdiff: render panicked (chart dir %q): %v", chartDir, r)
				done <- renderResult{err: fmt.Errorf("chartdiff: render panicked: %v", r)}
			}
		}()
		res, renderErr := e.renderer.Render(chartDir, nil)
		done <- renderResult{result: res, err: renderErr}
	}()

	timer := time.NewTimer(e.cfg.RenderTimeout)
	defer timer.Stop()

	select {
	case out := <-done:
		cleanup() // the render has fully finished; safe to remove chartDir now.
		return out.result, out.err, false
	case <-timer.C:
		go func() {
			<-done // wait for the abandoned render to actually stop touching chartDir
			cleanup()
		}()
		return nil, nil, true
	case <-ctx.Done():
		go func() {
			<-done
			cleanup()
		}()
		return nil, nil, true
	}
}
