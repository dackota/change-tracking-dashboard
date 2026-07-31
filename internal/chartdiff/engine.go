// Package chartdiff is the lazy compute engine for a Chart diff. Given a
// chart-kind Change (repo, tenant chart directory, commit SHA), Engine.Diff
// resolves old = the commit's first parent tree and new = the commit tree,
// renders both via a Renderer (chartrender), diffs the result via
// manifestdiff, and classifies any unavailability into one of a fixed, safe
// set of Outcome Kinds — never leaking internal Helm/git error detail to the
// caller.
//
// The materialize/cache/bounds orchestration behind that — an in-memory LRU
// cache (keyed by repo/tenant/parent-SHA/commit-SHA, storing failures too, so
// a known-bad render is never re-attempted), single-flight coalescing, a
// per-render timeout, a render concurrency cap, a dedicated materialize
// timeout and concurrency cap, and the materialization ceilings enforced in
// gitsource — lives in internal/subtree and is shared with plandiff. This
// package supplies only what is specific to a Chart diff: how to render a
// directory, how to diff two rendered manifest sets, and what this package's
// Outcome Kinds mean (see chartDomain in domain.go). Config below is this
// package's own public, documented bounds, translated into the engine's at
// construction.
package chartdiff

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
)

// instrumentationName scopes the tracer Engine obtains from the injected (or
// default global) TracerProvider — used for every downstream git/render call
// Engine.Diff's call graph makes (gitsource.first_parent,
// gitsource.materialize_subtree, chartrender.render).
const instrumentationName = "github.com/dackota/change-tracking-dashboard/internal/chartdiff"

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
// is safe for concurrent use by multiple goroutines.
type Engine struct {
	inner *subtree.Engine[manifestdiff.Manifest, Outcome]
}

// Option configures optional Engine dependencies (telemetry providers) at
// construction time. See WithTracerProvider.
type Option func(*options)

type options struct {
	tracerProvider trace.TracerProvider
}

// WithTracerProvider wires tp as the source of the tracer Engine.Diff uses
// for its own span and for every downstream git/render call's child span
// (gitsource.first_parent, gitsource.materialize_subtree,
// chartrender.render). Tests inject an sdktrace.TracerProvider backed by an
// in-memory exporter to assert on emitted spans without a real OTLP backend.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *options) { o.tracerProvider = tp }
}

// NewEngine constructs an Engine from cfg (resolved and validated via
// Config.Resolved) and renderer. A nil renderer defaults to the production
// adapter over chartrender.Render; tests inject a fake. Without a
// WithTracerProvider Option, tracing defaults to the ambient global OTel
// TracerProvider (a safe no-op until cmd/dashboard/main.go calls
// telemetry.Init and registers the real one).
func NewEngine(cfg Config, renderer Renderer, opts ...Option) (*Engine, error) {
	resolved, err := cfg.Resolved()
	if err != nil {
		return nil, err
	}

	if renderer == nil {
		renderer = helmRenderer{}
	}

	var o options
	for _, opt := range opts {
		opt(&o)
	}
	var coreOpts []subtree.Option
	if o.tracerProvider != nil {
		coreOpts = append(coreOpts, subtree.WithTracerProvider(o.tracerProvider))
	}

	inner, err := subtree.NewEngine[manifestdiff.Manifest, Outcome](
		subtree.Config{
			ProduceTimeout:            resolved.RenderTimeout,
			ConcurrencyCap:            resolved.ConcurrencyCap,
			CacheEntries:              resolved.CacheEntries,
			MaterializeTimeout:        resolved.MaterializeTimeout,
			MaterializeConcurrencyCap: resolved.MaterializeConcurrencyCap,
			Materialize: gitsource.MaterializeBounds{
				MaxTotalBytes: resolved.MaxMaterializedBytes,
				MaxFiles:      resolved.MaxMaterializedFiles,
				MaxDepth:      resolved.MaxMaterializedDepth,
				MaxTreeNodes:  resolved.MaxMaterializedNodes,
			},
			Labels: subtree.Labels{
				Name:                "chartdiff",
				ProduceVerb:         "render",
				ProduceSpanName:     "chartrender.render",
				InstrumentationName: instrumentationName,
			},
		},
		chartDomain{renderer: renderer, maxUnifiedBytes: resolved.MaxUnifiedBytes},
		coreOpts...,
	)
	if err != nil {
		return nil, err
	}
	return &Engine{inner: inner}, nil
}

// Diff computes (or returns the cached) Chart diff Outcome for req against
// repo. For any input it returns exactly one Outcome Kind, and a panic in
// either the materialize or the render step is recovered and classified. The
// one exception is a panic inside Repo.FirstParent, which is called
// synchronously and propagates — see subtree.Engine.Diff.
//
// Classification:
//   - repo.FirstParent reports gitsource.ErrNoParent (req.CommitSha is a
//     root commit) -> NoPriorVersion.
//   - materialization exceeds a configured bound
//     (gitsource.ErrMaterializeBoundsExceeded), or a materialize/render call
//     exceeds its configured timeout -> ExceededLimits.
//   - chartrender reports ReasonDependencyNotVendored -> Unavailable.
//   - chartrender reports ReasonMalformedChart, or any other unclassified
//     failure resolving/materializing/rendering either side (a generic, safe
//     bucket — the specific cause is logged server-side, never returned) ->
//     CouldNotRender.
//   - both sides render -> OK, with the manifestdiff.Result.
//
// See chartDomain.Classify for the mapping itself, and subtree.Engine.Diff
// for the single-flight cancellation limitation Diff inherits.
func (e *Engine) Diff(ctx context.Context, repo subtree.Repo, req Request) Outcome {
	return e.inner.Diff(ctx, repo, subtree.Request(req))
}
