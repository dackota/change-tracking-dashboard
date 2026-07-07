package chartdiff

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/manifestdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/telemetry"
)

// materializeAndRender materializes sha's tenant subtree into a fresh,
// caller-exclusive temp directory and renders it. ok is false when a
// terminal classified Outcome was reached (the caller should return it
// immediately); ok is true when manifests holds a successful render.
//
// This function is the single chokepoint for the temp-dir cleanup-lifecycle
// invariant (both compute's old-side and new-side calls flow through it):
// destDir is cleaned up exactly once on EVERY termination — success, a
// classified materialize error, materialize bounds-exceeded, a materialize
// timeout, a render timeout, ctx cancellation at any queued-or-in-flight
// point in either step, and a panic originating in either
// repo.MaterializeSubtreeBounded (guarded inside materializeBounded's
// goroutine) or the render (guarded inside renderBounded's goroutine) —
// never leaked, never removed while a step still reads it, never
// double-removed. materialize and render are protected identically
// (timeout + concurrency cap + goroutine-isolated panic recovery), closing
// the asymmetry a prior review flagged: an unbounded/uncancellable
// materialize step next to a bounded render step was itself the DoS gap.
func (e *Engine) materializeAndRender(ctx context.Context, repo ChartRepo, req Request, sha string, bounds gitsource.MaterializeBounds, side string) (manifests []manifestdiff.Manifest, outcome Outcome, ok bool) {
	destDir, cleanup, err := newExclusiveTempDir()
	if err != nil {
		telemetry.LoggerFromContext(ctx).Error("chartdiff: create temp materialize dir",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "error", err)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	// handedOff tracks whether ownership of destDir's cleanup has passed to
	// materializeBounded (only on its timeout/ctx-cancel path — see its
	// doc) or to renderBounded (unconditionally, once materialize has
	// finished) — whichever bounded step currently owns cleaning it up,
	// each cleaning up exactly once, only after any goroutine it started has
	// genuinely stopped touching destDir. Until a handoff happens, this
	// deferred guard is the safety net that fires cleanup on every OTHER way
	// this function can end — including a panic unwinding past it, since
	// deferred functions still run during a panic. Once handedOff is true
	// this guard is a no-op: whichever step took ownership must be the only
	// thing that removes destDir, so a still-running materialize or render
	// is never removed out from under itself (the HIGH-2 race this must not
	// reintroduce, now closed for both steps at the same chokepoint).
	handedOff := false
	defer func() {
		if !handedOff {
			cleanup()
		}
	}()

	materializeErr, materializeTimedOut := e.materializeBounded(ctx, repo, sha, req.TenantPath, destDir, bounds, cleanup)
	if materializeTimedOut {
		// materializeBounded has taken ownership of destDir's cleanup on
		// this path: its own background waiter cleans up once the abandoned
		// call actually finishes touching destDir (see materializeBounded's
		// doc) — this function must not clean up again.
		handedOff = true
		telemetry.LoggerFromContext(ctx).Error("chartdiff: materialize exceeded timeout",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "timeout", e.cfg.MaterializeTimeout)
		return nil, Outcome{Kind: ExceededLimits}, false
	}
	if materializeErr != nil {
		if errors.Is(materializeErr, gitsource.ErrMaterializeBoundsExceeded) {
			return nil, Outcome{Kind: ExceededLimits}, false
		}
		telemetry.LoggerFromContext(ctx).Error("chartdiff: materialize failed",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "error", materializeErr)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	// From here on, renderBounded owns destDir's cleanup: a render goroutine
	// may be started against it, and destDir must not be removed until that
	// goroutine has actually stopped touching it (see renderBounded's doc).
	handedOff = true
	result, err, timedOut := e.renderBounded(ctx, destDir, cleanup)
	if timedOut {
		telemetry.LoggerFromContext(ctx).Error("chartdiff: render exceeded timeout",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "timeout", e.cfg.RenderTimeout)
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
		telemetry.LoggerFromContext(ctx).Error("chartdiff: render failed",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "error", err)
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

// materializeBounded runs a single ChartRepo.MaterializeSubtreeBounded call
// under both the materialize concurrency semaphore and
// Config.MaterializeTimeout, against the caller-exclusive destDir
// materializeAndRender created for this side. It mirrors renderBounded's
// shape deliberately (see materializeAndRender's doc): materialize is, like
// render, a synchronous call over untrusted repository content with no
// cancellation hook (go-git's tree walk cannot be interrupted mid-walk any
// more than the Helm SDK's render can), so it needs the identical
// protection — a per-call timeout and its own concurrency cap — not a
// different shape.
//
// Acquiring the semaphore slot itself respects ctx, exactly like
// renderBounded's slot acquire: under a saturated queue (every
// MaterializeConcurrencyCap slot busy), a caller whose ctx is already
// cancelled or expires while queued for a slot must not block — the select
// below races the slot send against ctx.Done(), so cancellation while still
// queued is noticed immediately, before ever touching repo or the timeout
// timer. No materialize goroutine was ever started in that case, so cleanup
// runs directly, right there, via the cleanup func the caller passed in.
//
// Unlike renderBounded, materializeBounded does NOT always own destDir's
// cleanup: on the fast path (repo.MaterializeSubtreeBounded returns before
// the deadline, whether it succeeds or fails), destDir may still be needed
// for the render step that follows, so ownership of cleanup stays with the
// caller (materializeAndRender) — materializeBounded only reports (err,
// timedOut) in that case and never calls cleanup itself. On the
// timeout/ctx-cancellation path, however, the abandoned materialize
// goroutine keeps running and touching destDir until
// MaterializeSubtreeBounded itself returns — removing destDir out from under
// it would be the same HIGH-2-class race renderBounded's own doc describes
// — so materializeBounded takes ownership of cleanup on exactly that path
// (spawning a small waiter goroutine, exactly like renderBounded's
// timeout/ctx branches) and returns timedOut == true so the caller knows not
// to clean up destDir itself. Because done is a buffered, single-value
// channel, it is received from in exactly one place — either the select
// below or the waiter goroutine spawned on the timeout/cancel branch, never
// both — so cleanup (wherever it eventually runs) happens exactly once.
//
// The semaphore slot is released only when the goroutine itself finishes,
// mirroring renderBounded's own documented trade-off: a timed-out
// materialize still counts against MaterializeConcurrencyCap until it
// actually completes, which keeps the cap bounding real concurrent work
// rather than leaking unbounded goroutines.
func (e *Engine) materializeBounded(ctx context.Context, repo ChartRepo, sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds, cleanup func()) (err error, timedOut bool) {
	select {
	case e.materializeSem <- struct{}{}: // acquired a concurrency slot
	case <-ctx.Done():
		cleanup() // gave up queued for a slot; never touched materialize
		return nil, true
	}

	done := make(chan error, 1)
	go func() {
		defer func() { <-e.materializeSem }() // release only once materialize itself finishes
		// repo.MaterializeSubtreeBounded's own doc says it walks untrusted,
		// attacker-controlled repository content, so a go-git panic on a
		// corrupt or adversarial object is in threat model, not a
		// hypothetical. recover folds that into a plain error, which (like
		// any other unclassified materialize failure) falls through
		// materializeAndRender's classification to the safe CouldNotRender
		// bucket, rather than letting an unrecovered goroutine panic take
		// down the whole dashboard process.
		defer func() {
			if r := recover(); r != nil {
				telemetry.LoggerFromContext(ctx).Error("chartdiff: materialize panicked", "destDir", destDir, "panic", r)
				done <- fmt.Errorf("chartdiff: materialize panicked: %v", r)
			}
		}()
		// The real downstream git call, wrapped in its own child span
		// (criterion 5): a "subtree not found" or any other materialize
		// failure is recorded as a span exception with Error status here, at
		// the actual call site — not just logged — regardless of whether the
		// caller above has already given up on timeout. ctx is used only to
		// start the span (it may already be cancelled on the
		// abandoned-goroutine path); it does not gate this call, exactly
		// like every other use of ctx in this function.
		done <- telemetry.WithSpan(ctx, e.tracer, "gitsource.materialize_subtree", func(context.Context) error {
			return repo.MaterializeSubtreeBounded(sha, subtreePath, destDir, bounds)
		})
	}()

	timer := time.NewTimer(e.cfg.MaterializeTimeout)
	defer timer.Stop()

	select {
	case out := <-done:
		return out, false
	case <-timer.C:
		go func() {
			<-done // wait for the abandoned materialize call to actually stop touching destDir
			cleanup()
		}()
		return nil, true
	case <-ctx.Done():
		go func() {
			<-done
			cleanup()
		}()
		return nil, true
	}
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
				telemetry.LoggerFromContext(ctx).Error("chartdiff: render panicked", "chartDir", chartDir, "panic", r)
				done <- renderResult{err: fmt.Errorf("chartdiff: render panicked: %v", r)}
			}
		}()
		// The render call, wrapped in its own child span (criterion 2/5's
		// spirit — this is the third real downstream call in Diff's call
		// graph, alongside the two git calls): a malformed-chart or
		// dependency-not-vendored failure is recorded as a span exception
		// with Error status right here, at the actual call site. ctx is used
		// only to start the span (it may already be cancelled on the
		// abandoned-goroutine path); it does not gate this call, exactly
		// like every other use of ctx in this function.
		var res *chartrender.Result
		renderErr := telemetry.WithSpan(ctx, e.tracer, "chartrender.render", func(context.Context) error {
			var err error
			res, err = e.renderer.Render(chartDir, nil)
			return err
		})
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
