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
	defer cleanup()

	if err := repo.MaterializeSubtreeBounded(sha, req.TenantPath, destDir, bounds); err != nil {
		if errors.Is(err, gitsource.ErrMaterializeBoundsExceeded) {
			return nil, Outcome{Kind: ExceededLimits}, false
		}
		log.Printf("chartdiff: materialize (%s side) repo=%q tenant=%q sha=%q: %v", side, req.RepoName, req.TenantPath, sha, err)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	result, err, timedOut := e.renderBounded(ctx, destDir)
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
// concurrency semaphore and the per-render timeout.
//
// Render runs in its own goroutine because chartrender.Render is
// synchronous and cannot be interrupted mid-render (the Helm SDK has no
// cancellation hook). On timeout, renderBounded returns immediately
// (timedOut == true) so the caller isn't blocked — but the semaphore slot is
// released only when the goroutine itself finishes (the deferred release is
// inside the goroutine, not after the select), so a timed-out render still
// counts against ConcurrencyCap until it actually completes. This is a
// deliberate, documented trade-off: it keeps the cap bounding real
// concurrent Helm work rather than leaking unbounded goroutines, at the cost
// that a genuinely non-terminating render would eventually starve the
// semaphore. Real Helm templates terminate on finite input, so this is
// accepted as a v1 limitation (ADR 0002).
func (e *Engine) renderBounded(ctx context.Context, chartDir string) (result *chartrender.Result, err error, timedOut bool) {
	e.sem <- struct{}{} // acquire a concurrency slot, blocking until one is free

	done := make(chan renderResult, 1)
	go func() {
		defer func() { <-e.sem }() // release only once the render itself finishes
		res, renderErr := e.renderer.Render(chartDir, nil)
		done <- renderResult{result: res, err: renderErr}
	}()

	timer := time.NewTimer(e.cfg.RenderTimeout)
	defer timer.Stop()

	select {
	case out := <-done:
		return out.result, out.err, false
	case <-timer.C:
		return nil, nil, true
	case <-ctx.Done():
		return nil, nil, true
	}
}
