package plandiff

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// materializeAndParse materializes sha's Terraform stack/module subtree into
// a fresh, caller-exclusive temp directory and parses it. ok is false when a
// terminal classified Outcome was reached (the caller should return it
// immediately); ok is true when resources holds a successful parse.
//
// This function is the single chokepoint for the temp-dir cleanup-lifecycle
// invariant (both compute's old-side and new-side calls flow through it):
// destDir is cleaned up exactly once on EVERY termination -- success, a
// classified materialize error, materialize bounds-exceeded, a materialize
// timeout, a parse timeout, ctx cancellation at any queued-or-in-flight
// point in either step, and a panic originating in either
// repo.MaterializeSubtreeBounded (guarded inside materializeBounded's
// goroutine) or the parse (guarded inside parseBounded's goroutine) -- never
// leaked, never removed while a step still reads it, never double-removed.
// materialize and parse are protected identically (timeout + concurrency cap
// + goroutine-isolated panic recovery) -- this mirrors
// chartdiff.materializeAndRender's cleanup-lifecycle chokepoint exactly, for
// the same reason: an unbounded/uncancellable step next to a bounded one is
// itself the DoS gap chartdiff's own history already found and closed once.
func (e *Engine) materializeAndParse(ctx context.Context, repo PlanRepo, req Request, sha string, bounds gitsource.MaterializeBounds, side string) (resources []Resource, outcome Outcome, ok bool) {
	destDir, cleanup, err := newExclusiveTempDir()
	if err != nil {
		telemetry.LoggerFromContext(ctx).Error("plandiff: create temp materialize dir",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "error", err)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	// handedOff tracks whether ownership of destDir's cleanup has passed to
	// materializeBounded (only on its timeout/ctx-cancel path) or to
	// parseBounded (unconditionally, once materialize has finished) --
	// whichever bounded step currently owns cleaning it up, each cleaning up
	// exactly once, only after any goroutine it started has genuinely
	// stopped touching destDir. Until a handoff happens, this deferred guard
	// fires cleanup on every OTHER way this function can end -- including a
	// panic unwinding past it, since deferred functions still run during a
	// panic.
	handedOff := false
	defer func() {
		if !handedOff {
			cleanup()
		}
	}()

	materializeErr, materializeTimedOut := e.materializeBounded(ctx, repo, sha, req.TenantPath, destDir, bounds, cleanup)
	if materializeTimedOut {
		handedOff = true
		telemetry.LoggerFromContext(ctx).Error("plandiff: materialize exceeded timeout",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "timeout", e.cfg.MaterializeTimeout)
		return nil, Outcome{Kind: ExceededLimits}, false
	}
	if materializeErr != nil {
		if errors.Is(materializeErr, gitsource.ErrMaterializeBoundsExceeded) {
			return nil, Outcome{Kind: ExceededLimits}, false
		}
		telemetry.LoggerFromContext(ctx).Error("plandiff: materialize failed",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "error", materializeErr)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	// From here on, parseBounded owns destDir's cleanup.
	handedOff = true
	result, err, timedOut := e.parseBounded(ctx, destDir, cleanup)
	if timedOut {
		telemetry.LoggerFromContext(ctx).Error("plandiff: parse exceeded timeout",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "timeout", e.cfg.ParseTimeout)
		return nil, Outcome{Kind: ExceededLimits}, false
	}
	if err != nil {
		if errors.Is(err, errBlockDepthExceeded) {
			return nil, Outcome{Kind: ExceededLimits}, false
		}
		telemetry.LoggerFromContext(ctx).Error("plandiff: parse failed",
			"side", side, "repo", req.RepoName, "tenant", req.TenantPath, "sha", sha, "error", err)
		return nil, Outcome{Kind: CouldNotRender}, false
	}

	return result, Outcome{}, true
}

// newExclusiveTempDir creates a fresh, unpredictable-named, caller-exclusive
// temp directory (mirroring chartdiff's identical helper and its
// TOCTOU/symlink-follow rationale) for a single materialize+parse.
func newExclusiveTempDir() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "plandiff-*")
	if err != nil {
		return "", nil, fmt.Errorf("plandiff: create temp materialize dir: %w", err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// materializeBounded runs a single PlanRepo.MaterializeSubtreeBounded call
// under both the materialize concurrency semaphore and
// Config.MaterializeTimeout, against the caller-exclusive destDir
// materializeAndParse created for this side. This is a byte-for-byte mirror
// of chartdiff.materializeBounded's shape and lifecycle guarantees -- see
// its doc for the full rationale, unchanged here.
func (e *Engine) materializeBounded(ctx context.Context, repo PlanRepo, sha, subtreePath, destDir string, bounds gitsource.MaterializeBounds, cleanup func()) (err error, timedOut bool) {
	select {
	case e.materializeSem <- struct{}{}: // acquired a concurrency slot
	case <-ctx.Done():
		cleanup() // gave up queued for a slot; never touched materialize
		return nil, true
	}

	done := make(chan error, 1)
	go func() {
		defer func() { <-e.materializeSem }() // release only once materialize itself finishes
		defer func() {
			if r := recover(); r != nil {
				telemetry.LoggerFromContext(ctx).Error("plandiff: materialize panicked", "destDir", destDir, "panic", r)
				done <- fmt.Errorf("plandiff: materialize panicked: %v", r)
			}
		}()
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

// parseResult carries a parse goroutine's outcome back to parseBounded's
// select.
type parseResult struct {
	resources []Resource
	err       error
}

// parseBounded runs a single Parser.Parse call under both the concurrency
// semaphore and the per-diff parse timeout, against the caller-exclusive
// directory materializeAndParse created -- and owns cleaning that directory
// up via cleanup exactly once, at the right time. This is a byte-for-byte
// mirror of chartdiff.renderBounded's shape and lifecycle guarantees -- see
// its doc for the full rationale, unchanged here.
func (e *Engine) parseBounded(ctx context.Context, dir string, cleanup func()) (resources []Resource, err error, timedOut bool) {
	select {
	case e.sem <- struct{}{}: // acquired a concurrency slot
	case <-ctx.Done():
		cleanup() // gave up queued for a slot; never touched the parser
		return nil, nil, true
	}

	done := make(chan parseResult, 1)
	go func() {
		defer func() { <-e.sem }() // release only once the parse itself finishes
		defer func() {
			if r := recover(); r != nil {
				telemetry.LoggerFromContext(ctx).Error("plandiff: parse panicked", "dir", dir, "panic", r)
				done <- parseResult{err: fmt.Errorf("plandiff: parse panicked: %v", r)}
			}
		}()
		var res []Resource
		parseErr := telemetry.WithSpan(ctx, e.tracer, "plandiff.parse", func(context.Context) error {
			var err error
			res, err = e.parser.Parse(dir)
			return err
		})
		done <- parseResult{resources: res, err: parseErr}
	}()

	timer := time.NewTimer(e.cfg.ParseTimeout)
	defer timer.Stop()

	select {
	case out := <-done:
		cleanup() // the parse has fully finished; safe to remove dir now.
		return out.resources, out.err, false
	case <-timer.C:
		go func() {
			<-done // wait for the abandoned parse to actually stop touching dir
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
