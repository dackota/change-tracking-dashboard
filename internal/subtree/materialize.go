package subtree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// materializeAndProduce materializes sha's tenant subtree into a fresh,
// caller-exclusive temp directory and runs Domain.Produce against it. ok is
// false when a terminal Failure was reached (the caller should classify and
// return it immediately); ok is true when artifacts holds a successful
// Produce.
//
// This function is the single chokepoint for the temp-dir cleanup-lifecycle
// invariant (both compute's old-side and new-side calls flow through it):
// destDir is cleaned up exactly once on EVERY termination — success, a
// classified materialize error, materialize bounds-exceeded, a materialize
// timeout, a produce timeout, ctx cancellation at any queued-or-in-flight
// point in either step, and a panic originating in either
// Repo.MaterializeSubtreeBounded (guarded inside materializeBounded's
// goroutine) or Domain.Produce (guarded inside produceBounded's goroutine) —
// never leaked, never removed while a step still reads it, never
// double-removed. Materialize and produce are protected identically (timeout
// + concurrency cap + goroutine-isolated panic recovery): an
// unbounded/uncancellable step next to a bounded one is itself the DoS gap.
func (e *Engine[A, O]) materializeAndProduce(ctx context.Context, repo Repo, req Request, sha, side string) (artifacts []A, failure Failure, ok bool) {
	name := e.cfg.Labels.Name

	destDir, cleanup, err := e.newExclusiveTempDir()
	if err != nil {
		telemetry.LoggerFromContext(ctx).Error(name+": create temp materialize dir",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "error", err)
		return nil, Failure{Kind: Internal, Side: side, Sha: sha, Req: req}, false
	}

	// handedOff tracks whether ownership of destDir's cleanup has passed to
	// materializeBounded (only on its timeout/ctx-cancel path — see its doc)
	// or to produceBounded (unconditionally, once materialize has finished) —
	// whichever bounded step currently owns cleaning it up, each cleaning up
	// exactly once, only after any goroutine it started has genuinely stopped
	// touching destDir. Until a handoff happens, this deferred guard is the
	// safety net that fires cleanup on every OTHER way this function can end
	// — including a panic unwinding past it, since deferred functions still
	// run during a panic. Once handedOff is true this guard is a no-op:
	// whichever step took ownership must be the only thing that removes
	// destDir, so a still-running materialize or produce is never removed out
	// from under itself.
	handedOff := false
	defer func() {
		if !handedOff {
			cleanup()
		}
	}()

	materializeErr, materializeTimedOut := e.materializeBounded(ctx, repo, sha, req.TenantPath, destDir, cleanup)
	if materializeTimedOut {
		// materializeBounded has taken ownership of destDir's cleanup on this
		// path: its own background waiter cleans up once the abandoned call
		// actually finishes touching destDir (see materializeBounded's doc) —
		// this function must not clean up again.
		handedOff = true
		telemetry.LoggerFromContext(ctx).Error(name+": materialize exceeded timeout",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "timeout", e.cfg.MaterializeTimeout)
		return nil, Failure{Kind: ExceededLimits, Side: side, Sha: sha, Req: req}, false
	}
	if materializeErr != nil {
		if errors.Is(materializeErr, gitsource.ErrMaterializeBoundsExceeded) {
			return nil, Failure{Kind: ExceededLimits, Side: side, Sha: sha, Req: req}, false
		}
		telemetry.LoggerFromContext(ctx).Error(name+": materialize failed",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "error", materializeErr)
		return nil, Failure{Kind: Internal, Side: side, Sha: sha, Req: req}, false
	}

	// From here on, produceBounded owns destDir's cleanup: a produce
	// goroutine may be started against it, and destDir must not be removed
	// until that goroutine has actually stopped touching it (see
	// produceBounded's doc).
	handedOff = true
	result, err, timedOut := e.produceBounded(ctx, destDir, cleanup)
	if timedOut {
		telemetry.LoggerFromContext(ctx).Error(name+": "+e.cfg.Labels.ProduceVerb+" exceeded timeout",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "timeout", e.cfg.ProduceTimeout)
		return nil, Failure{Kind: ExceededLimits, Side: side, Sha: sha, Req: req}, false
	}
	if err != nil {
		// The domain owns its own error vocabulary: hand the cause back
		// unwrapped and let Classify decide what it means and whether it is
		// worth logging. The engine deliberately does not log here — a domain
		// may classify some produce failures as expected outcomes.
		return nil, Failure{Kind: ProduceFailed, Cause: err, Side: side, Sha: sha, Req: req}, false
	}

	return result, Failure{}, true
}

// newExclusiveTempDir creates a fresh, unpredictable-named, caller-exclusive
// temp directory (os.MkdirTemp's documented mode is 0700, subject to umask —
// never more permissive) for a single materialize+produce. Using a freshly
// created, exclusively-owned directory per computation — never a
// caller-supplied or tenant-derived path — closes the TOCTOU/symlink-follow
// risk of a shared or externally-writable destination directory: a
// world-readable file written under a 0700 directory is still unreachable to
// any other user on the host.
func (e *Engine[A, O]) newExclusiveTempDir() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", e.cfg.Labels.Name+"-*")
	if err != nil {
		return "", nil, fmt.Errorf("%s: create temp materialize dir: %w", e.cfg.Labels.Name, err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// materializeBounded runs a single Repo.MaterializeSubtreeBounded call under
// both the materialize concurrency semaphore and Config.MaterializeTimeout,
// against the caller-exclusive destDir materializeAndProduce created for this
// side. It mirrors produceBounded's shape deliberately: materialize is, like
// produce, a synchronous call over untrusted repository content with no
// cancellation hook (go-git's tree walk cannot be interrupted mid-walk), so
// it needs the identical protection — a per-call timeout and its own
// concurrency cap — not a different shape.
//
// Acquiring the semaphore slot itself respects ctx, exactly like
// produceBounded's slot acquire: under a saturated queue (every
// MaterializeConcurrencyCap slot busy), a caller whose ctx is already
// cancelled or expires while queued for a slot must not block — the select
// below races the slot send against ctx.Done(), so cancellation while still
// queued is noticed immediately, before ever touching repo or the timeout
// timer. No materialize goroutine was ever started in that case, so cleanup
// runs directly, right there, via the cleanup func the caller passed in.
//
// Unlike produceBounded, materializeBounded does NOT always own destDir's
// cleanup: on the fast path (MaterializeSubtreeBounded returns before the
// deadline, whether it succeeds or fails), destDir may still be needed for
// the produce step that follows, so ownership of cleanup stays with the
// caller — materializeBounded only reports (err, timedOut) in that case and
// never calls cleanup itself. On the timeout/ctx-cancellation path, however,
// the abandoned materialize goroutine keeps running and touching destDir
// until MaterializeSubtreeBounded itself returns — removing destDir out from
// under it would be a use-after-free-class race — so materializeBounded takes
// ownership of cleanup on exactly that path (spawning a small waiter
// goroutine, exactly like produceBounded's timeout/ctx branches) and returns
// timedOut == true so the caller knows not to clean up destDir itself.
// Because done is a buffered, single-value channel, it is received from in
// exactly one place — either the select below or the waiter goroutine spawned
// on the timeout/cancel branch, never both — so cleanup (wherever it
// eventually runs) happens exactly once.
//
// The semaphore slot is released only when the goroutine itself finishes,
// mirroring produceBounded's own documented trade-off: a timed-out
// materialize still counts against MaterializeConcurrencyCap until it
// actually completes, which keeps the cap bounding real concurrent work
// rather than leaking unbounded goroutines.
func (e *Engine[A, O]) materializeBounded(ctx context.Context, repo Repo, sha, subtreePath, destDir string, cleanup func()) (err error, timedOut bool) {
	select {
	case e.materializeSem <- struct{}{}: // acquired a concurrency slot
	case <-ctx.Done():
		cleanup() // gave up queued for a slot; never touched materialize
		return nil, true
	}

	done := make(chan error, 1)
	go func() {
		defer func() { <-e.materializeSem }() // release only once materialize itself finishes
		// Repo.MaterializeSubtreeBounded's own doc says it walks untrusted,
		// attacker-controlled repository content, so a go-git panic on a
		// corrupt or adversarial object is in threat model, not a
		// hypothetical. recover folds that into a plain error, which (like any
		// other unclassified materialize failure) falls through to the safe
		// Internal bucket, rather than letting an unrecovered goroutine panic
		// take down the whole dashboard process.
		defer func() {
			if r := recover(); r != nil {
				telemetry.LoggerFromContext(ctx).Error(e.cfg.Labels.Name+": materialize panicked", "destDir", destDir, "panic", r)
				done <- fmt.Errorf("%s: materialize panicked: %v", e.cfg.Labels.Name, r)
			}
		}()
		// The real downstream git call, wrapped in its own child span: a
		// "subtree not found" or any other materialize failure is recorded as
		// a span exception with Error status here, at the actual call site —
		// not just logged — regardless of whether the caller above has already
		// given up on timeout. ctx is used only to start the span (it may
		// already be cancelled on the abandoned-goroutine path); it does not
		// gate this call.
		done <- telemetry.WithSpan(ctx, e.tracer, "gitsource.materialize_subtree", func(context.Context) error {
			return repo.MaterializeSubtreeBounded(sha, subtreePath, destDir, e.cfg.Materialize)
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

// produceResult carries a produce goroutine's outcome back to
// produceBounded's select.
type produceResult[A any] struct {
	artifacts []A
	err       error
}

// produceBounded runs a single Domain.Produce call under both the concurrency
// semaphore and Config.ProduceTimeout, against the caller-exclusive dir
// materializeAndProduce created — and owns cleaning that directory up via
// cleanup exactly once, at the right time (see below).
//
// Acquiring the semaphore slot itself respects ctx: under a saturated queue
// (every ConcurrencyCap slot busy), a caller whose ctx is already cancelled
// or expires while queued for a slot must not block on the acquire — the
// select below races the slot send against ctx.Done(), so cancellation while
// still queued is noticed immediately, before ever touching the domain or the
// timeout timer. No produce goroutine was ever started in that case, so
// cleanup runs directly, right there.
//
// Produce runs in its own goroutine because a Domain's implementation is
// synchronous and cannot be interrupted mid-call (neither the Helm SDK nor
// the HCL parser has a cancellation hook). On timeout or ctx cancellation,
// produceBounded returns immediately (timedOut == true) so the caller isn't
// blocked — but the goroutine keeps running against dir until Produce itself
// returns, so cleanup must not run yet: doing so would remove dir out from
// under a call that is still reading it. Because done is a buffered,
// single-value channel, it is received from in exactly one place: either the
// select below (the fast path, once Produce finishes before the deadline) or
// the small worker goroutine spawned on the timeout/cancel branch (which
// blocks on <-done until the abandoned call actually finishes, then cleans
// up) — never both. This guarantees cleanup happens exactly once, and only
// after the goroutine has genuinely stopped touching dir, without needing a
// sync.Once or shared mutable flag to coordinate the two paths.
//
// The semaphore slot is released only when the goroutine itself finishes (the
// deferred release is inside the goroutine, not after the select), so a
// timed-out Produce still counts against ConcurrencyCap until it actually
// completes. This is a deliberate, documented trade-off: it keeps the cap
// bounding real concurrent work rather than leaking unbounded goroutines, at
// the cost that a genuinely non-terminating Produce would eventually starve
// the semaphore. Real Helm templates and HCL files terminate on finite input,
// so this is accepted as a v1 limitation.
func (e *Engine[A, O]) produceBounded(ctx context.Context, dir string, cleanup func()) (artifacts []A, err error, timedOut bool) {
	select {
	case e.sem <- struct{}{}: // acquired a concurrency slot
	case <-ctx.Done():
		cleanup() // gave up queued for a slot; never touched the domain
		return nil, nil, true
	}

	done := make(chan produceResult[A], 1)
	go func() {
		defer func() { <-e.sem }() // release only once the produce itself finishes
		// Hostile or malformed content could trigger a panic deep in a
		// domain's dependency, well outside this package's control. recover
		// folds that into a plain error, which the domain then classifies like
		// any other produce failure, rather than letting an unrecovered
		// goroutine panic take down the whole dashboard process.
		defer func() {
			if r := recover(); r != nil {
				telemetry.LoggerFromContext(ctx).Error(e.cfg.Labels.Name+": "+e.cfg.Labels.ProduceVerb+" panicked", "dir", dir, "panic", r)
				done <- produceResult[A]{err: fmt.Errorf("%s: %s panicked: %v", e.cfg.Labels.Name, e.cfg.Labels.ProduceVerb, r)}
			}
		}()
		// The produce call, wrapped in its own child span: a domain-classified
		// failure is recorded as a span exception with Error status right
		// here, at the actual call site. ctx is used only to start the span
		// (it may already be cancelled on the abandoned-goroutine path); it
		// does not gate this call.
		var res []A
		produceErr := telemetry.WithSpan(ctx, e.tracer, e.cfg.Labels.ProduceSpanName, func(context.Context) error {
			var err error
			res, err = e.domain.Produce(dir)
			return err
		})
		done <- produceResult[A]{artifacts: res, err: produceErr}
	}()

	timer := time.NewTimer(e.cfg.ProduceTimeout)
	defer timer.Stop()

	select {
	case out := <-done:
		cleanup() // the produce has fully finished; safe to remove dir now.
		return out.artifacts, out.err, false
	case <-timer.C:
		go func() {
			<-done // wait for the abandoned produce to actually stop touching dir
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
