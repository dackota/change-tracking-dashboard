// Package chartdiff is the lazy compute engine for a Chart diff (see
// CONTEXT.md and ADR 0002). Given a chart-kind Change (repo, tenant chart
// directory, commit SHA), Engine.Diff resolves old = the commit's first
// parent tree and new = the commit tree via a ChartRepo (gitsource), renders
// both via a Renderer (chartrender), diffs the result via manifestdiff, and
// classifies any unavailability into one of a fixed, safe set of Outcome
// Kinds — never leaking internal Helm/git error detail to the caller.
//
// Engine owns ADR 0002's resource bounds: an in-memory LRU cache (keyed by
// repo/tenant/parent-SHA/commit-SHA, storing failures too, so a known-bad
// render is never re-attempted), a per-render timeout, a render concurrency
// cap (semaphore), and the manifestdiff output size ceiling. See Config.
package chartdiff

import (
	"context"
	"errors"
	"fmt"
	"log"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/manifestdiff"
)

// Request identifies one Chart diff to compute: the tenant chart directory
// (the directory of the chart Change's Chart.yaml, relative to the repo
// root) at CommitSha, diffed against its first parent. RepoName feeds the
// cache key, distinguishing two repos that happen to share a tenant path.
type Request struct {
	// RepoName identifies the repo req.CommitSha belongs to.
	RepoName string
	// TenantPath is the tenant chart directory, relative to the repo root.
	TenantPath string
	// CommitSha is the chart-kind change commit.
	CommitSha string
}

// Engine is the Chart diff compute engine. Construct one with NewEngine; it
// is safe for concurrent use by multiple goroutines (its cache, semaphore,
// and single-flight group are all internally synchronized).
type Engine struct {
	cfg      Config
	renderer Renderer
	cache    *lru.Cache[cacheKey, Outcome]
	sem      chan struct{}
	group    singleflight.Group
}

// NewEngine constructs an Engine from cfg (resolved and validated via
// Config.Resolved) and renderer. A nil renderer defaults to the production
// adapter over chartrender.Render; tests inject a fake.
func NewEngine(cfg Config, renderer Renderer) (*Engine, error) {
	resolved, err := cfg.Resolved()
	if err != nil {
		return nil, err
	}

	cache, err := lru.New[cacheKey, Outcome](resolved.CacheEntries)
	if err != nil {
		return nil, fmt.Errorf("chartdiff: create cache: %w", err)
	}

	if renderer == nil {
		renderer = helmRenderer{}
	}

	return &Engine{
		cfg:      resolved,
		renderer: renderer,
		cache:    cache,
		sem:      make(chan struct{}, resolved.ConcurrencyCap),
	}, nil
}

// Diff computes (or returns the cached) Chart diff Outcome for req against
// repo. It is a total function: for any input, exactly one Outcome Kind is
// returned and Diff never panics.
//
// Classification:
//   - repo.FirstParent reports gitsource.ErrNoParent (req.CommitSha is a
//     root commit) -> NoPriorVersion.
//   - materialization exceeds a configured bound
//     (gitsource.ErrMaterializeBoundsExceeded), or a render exceeds
//     Config.RenderTimeout -> ExceededLimits.
//   - chartrender reports ReasonDependencyNotVendored -> Unavailable.
//   - chartrender reports ReasonMalformedChart, or any other unclassified
//     failure resolving/materializing/rendering either side (a generic,
//     safe bucket — the specific cause is logged server-side, never
//     returned) -> CouldNotRender.
//   - both sides render -> OK, with the manifestdiff.Result.
//
// Known limitation: e.group.Do coalesces concurrent Diff calls for the same
// key onto a single computation, which runs under only the *leader's* ctx
// (the caller whose call actually triggered computeAndCache). A follower
// call coalesced onto that in-flight computation waits for it to finish
// regardless of its own ctx being cancelled — singleflight has no per-caller
// cancellation. This is inherent to singleflight, pre-existing, and not
// addressed here; it does not affect the leader, and a follower is still
// bounded by the leader's own render timeout / bounds checks.
func (e *Engine) Diff(ctx context.Context, repo ChartRepo, req Request) Outcome {
	parentSha, err := repo.FirstParent(req.CommitSha)
	if err != nil {
		if errors.Is(err, gitsource.ErrNoParent) {
			return Outcome{Kind: NoPriorVersion}
		}
		log.Printf("chartdiff: resolve first parent repo=%q tenant=%q commit=%q: %v", req.RepoName, req.TenantPath, req.CommitSha, err)
		return Outcome{Kind: CouldNotRender}
	}

	key := cacheKey{repoName: req.RepoName, tenantPath: req.TenantPath, parentSha: parentSha, commitSha: req.CommitSha}

	if cached, ok := e.cache.Get(key); ok {
		return cached
	}

	// group.Do coalesces concurrent Diff calls for the same key into a
	// single computation: only the first caller materializes and renders,
	// every concurrent caller for the same key shares its result. This is
	// what keeps the renderer invocation count at "at most once per key"
	// even under a concurrent burst of identical requests, not just on the
	// already-cached fast path above.
	v, _, _ := e.group.Do(key.String(), func() (interface{}, error) {
		return e.computeAndCache(ctx, repo, req, key), nil
	})
	return v.(Outcome)
}

// computeAndCache re-checks the cache (closing the race between Diff's cache
// check and this call joining the single-flight group — another goroutine
// may have already populated the cache in between), computes the Outcome on
// a genuine miss, and caches it (including a classified failure) before
// returning.
func (e *Engine) computeAndCache(ctx context.Context, repo ChartRepo, req Request, key cacheKey) Outcome {
	if cached, ok := e.cache.Get(key); ok {
		return cached
	}

	outcome := e.compute(ctx, repo, req, key.parentSha)
	e.cache.Add(key, outcome)
	return outcome
}

// compute materializes and renders both sides of the diff and returns the
// classified Outcome. It never touches the cache; computeAndCache owns that.
func (e *Engine) compute(ctx context.Context, repo ChartRepo, req Request, parentSha string) Outcome {
	bounds := gitsource.MaterializeBounds{
		MaxTotalBytes: e.cfg.MaxMaterializedBytes,
		MaxFiles:      e.cfg.MaxMaterializedFiles,
		MaxDepth:      e.cfg.MaxMaterializedDepth,
	}

	oldManifests, failure, ok := e.materializeAndRender(ctx, repo, req, parentSha, bounds, "old")
	if !ok {
		return failure
	}

	newManifests, failure, ok := e.materializeAndRender(ctx, repo, req, req.CommitSha, bounds, "new")
	if !ok {
		return failure
	}

	diff := manifestdiff.Diff(manifestdiff.Params{
		Old:             oldManifests,
		New:             newManifests,
		MaxUnifiedBytes: e.cfg.MaxUnifiedBytes,
	})

	return Outcome{Kind: OK, Diff: diff}
}
