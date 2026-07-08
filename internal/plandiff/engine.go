// Package plandiff is the lazy, credential-free compute engine for a static
// Terraform plan-diff (see CONTEXT.md and the terraform-change-tracking PRD,
// R17-R22). Given a Terraform-kind Request (repo, stack/module directory,
// commit SHA), Engine.Diff resolves old = the commit's first parent tree and
// new = the commit tree via a PlanRepo (gitsource), parses both sides' HCL
// via a Parser into a Resource set, classifies the resource-level delta
// (added/removed/changed, with a replacement-forcing heuristic), renders it
// through manifestdiff, and classifies any unavailability into one of a
// fixed, safe set of Outcome Kinds -- never leaking internal git/HCL-parser
// error detail to the caller.
//
// plandiff mirrors chartdiff.Engine's shape deliberately (R20: "the same
// resource bounds as chartdiff"): an in-memory LRU cache (keyed by
// repo/path/parent-SHA/commit-SHA, storing failures too), a per-parse
// timeout, a parse concurrency cap (semaphore), a dedicated materialize
// concurrency cap and timeout, and the manifestdiff output size ceiling. See
// Config. Unlike chartdiff, plandiff never executes `terraform plan` or
// `terraform show -json` and never touches cloud credentials or state
// (acceptance criterion 3, PRD R19) -- its only inputs are HCL bytes
// PlanRepo materializes from git, parsed entirely in-process.
package plandiff

import (
	"context"
	"errors"
	"fmt"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// instrumentationName scopes the tracer Engine obtains from the injected (or
// default global) TracerProvider -- used for every downstream git/parse call
// Engine.Diff's call graph makes (gitsource.first_parent,
// gitsource.materialize_subtree, plandiff.parse; criterion 9).
const instrumentationName = "github.com/dackota/change-tracking-dashboard/internal/plandiff"

// Request identifies one Terraform plan-diff to compute: the stack/module
// directory (the directory containing the Terraform changeset's .tf files,
// relative to the repo root) at CommitSha, diffed against its first parent.
// RepoName feeds the cache key, distinguishing two repos that happen to
// share a stack path.
type Request struct {
	// RepoName identifies the repo req.CommitSha belongs to.
	RepoName string
	// TenantPath is the Terraform stack/module directory, relative to the
	// repo root.
	TenantPath string
	// CommitSha is the Terraform-kind change commit.
	CommitSha string
}

// Engine is the static Terraform plan-diff compute engine. Construct one
// with NewEngine; it is safe for concurrent use by multiple goroutines (its
// cache, semaphores, and single-flight group are all internally
// synchronized).
type Engine struct {
	cfg    Config
	parser Parser
	cache  *lru.Cache[cacheKey, Outcome]
	// sem bounds concurrent parse invocations (Config.ConcurrencyCap).
	sem chan struct{}
	// materializeSem bounds concurrent PlanRepo.MaterializeSubtreeBounded
	// invocations (Config.MaterializeConcurrencyCap) -- a dedicated
	// semaphore, not shared with sem, mirroring chartdiff's identical
	// rationale (materialize is a disk/CPU tree walk; parse is a CPU-bound
	// HCL-parse -- different resource profiles, independently tunable
	// ceilings).
	materializeSem chan struct{}
	group          singleflight.Group
	// tracer wraps every downstream git/parse call Diff's call graph makes
	// in its own child span (telemetry.WithSpan) -- see WithTracerProvider.
	tracer trace.Tracer
	// outcomeRecorder reports every Diff outcome's Kind for the poll-health
	// surface (acceptance criterion 9) -- see WithOutcomeRecorder.
	outcomeRecorder OutcomeRecorder
}

// OutcomeRecorder is the seam through which Engine.Diff reports the Kind of
// every Outcome it produces (including cache hits), for the poll-health/
// status surface (acceptance criterion 9). *pollstatus.Registry satisfies
// this directly.
type OutcomeRecorder interface {
	RecordPlanDiffOutcome(kind string)
}

// noopOutcomeRecorder is the default OutcomeRecorder for an Engine built
// without WithOutcomeRecorder, so Diff never needs a nil check.
type noopOutcomeRecorder struct{}

func (noopOutcomeRecorder) RecordPlanDiffOutcome(string) {}

// Option configures optional Engine dependencies (telemetry providers, the
// outcome recorder) at construction time.
type Option func(*Engine)

// WithTracerProvider wires tp as the source of the tracer Engine.Diff uses
// for its own span and for every downstream git/parse call's child span.
// Tests inject an sdktrace.TracerProvider backed by an in-memory exporter to
// assert on emitted spans without a real OTLP backend.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(e *Engine) {
		e.tracer = tp.Tracer(instrumentationName)
	}
}

// WithOutcomeRecorder wires rec as the destination for every Diff outcome's
// Kind (acceptance criterion 9). Without this Option, outcomes are recorded
// nowhere -- Diff's return value is unaffected either way.
func WithOutcomeRecorder(rec OutcomeRecorder) Option {
	return func(e *Engine) {
		e.outcomeRecorder = rec
	}
}

// NewEngine constructs an Engine from cfg (resolved and validated via
// Config.Resolved) and parser. A nil parser defaults to the production
// adapter (defaultParser, walking the filesystem); tests inject a fake.
// Without a WithTracerProvider Option, tracing defaults to the ambient
// global OTel TracerProvider (a safe no-op until telemetry.Init registers
// the real one).
func NewEngine(cfg Config, parser Parser, opts ...Option) (*Engine, error) {
	resolved, err := cfg.Resolved()
	if err != nil {
		return nil, err
	}

	cache, err := lru.New[cacheKey, Outcome](resolved.CacheEntries)
	if err != nil {
		return nil, fmt.Errorf("plandiff: create cache: %w", err)
	}

	if parser == nil {
		parser = defaultParser{maxBlockDepth: resolved.MaxBlockDepth}
	}

	e := &Engine{
		cfg:             resolved,
		parser:          parser,
		cache:           cache,
		sem:             make(chan struct{}, resolved.ConcurrencyCap),
		materializeSem:  make(chan struct{}, resolved.MaterializeConcurrencyCap),
		tracer:          otel.GetTracerProvider().Tracer(instrumentationName),
		outcomeRecorder: noopOutcomeRecorder{},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// Diff computes (or returns the cached) plan-diff Outcome for req against
// repo. It is a total function: for any input, exactly one Outcome Kind is
// returned and Diff never panics.
//
// Classification:
//   - repo.FirstParent reports gitsource.ErrNoParent (req.CommitSha is a
//     root commit) -> NoPriorVersion.
//   - materialization exceeds a configured bound
//     (gitsource.ErrMaterializeBoundsExceeded), a materialize/parse call
//     exceeds its configured timeout, or a resource body's nested-block
//     recursion exceeds Config.MaxBlockDepth -> ExceededLimits.
//   - any other unclassified failure resolving/materializing/parsing either
//     side (a generic, safe bucket -- the specific cause is logged
//     server-side, never returned) -> CouldNotRender.
//   - both sides parse -> OK, with the resource-level Summary/Resources and
//     the manifestdiff.Result.
//
// Every returned Outcome's Kind is reported to the configured
// OutcomeRecorder (acceptance criterion 9), including on the cache-hit and
// single-flight-follower fast paths -- so the poll-health surface's counts
// reflect every Diff call this Engine has ever served, not just genuine
// computations.
//
// Known limitation: e.group.Do coalesces concurrent Diff calls for the same
// key onto a single computation, which runs under only the *leader's* ctx.
// A follower call coalesced onto that in-flight computation waits for it to
// finish regardless of its own ctx being cancelled -- singleflight has no
// per-caller cancellation. This is inherent to singleflight, mirrors
// chartdiff.Engine.Diff's identical documented limitation, and does not
// affect the leader; a follower is still bounded by the leader's own parse
// timeout / bounds checks.
func (e *Engine) Diff(ctx context.Context, repo PlanRepo, req Request) Outcome {
	outcome := e.diff(ctx, repo, req)
	e.outcomeRecorder.RecordPlanDiffOutcome(string(outcome.Kind))
	return outcome
}

func (e *Engine) diff(ctx context.Context, repo PlanRepo, req Request) Outcome {
	var parentSha string
	err := telemetry.WithSpan(ctx, e.tracer, "gitsource.first_parent", func(context.Context) error {
		v, err := repo.FirstParent(req.CommitSha)
		parentSha = v
		return err
	})
	if err != nil {
		if errors.Is(err, gitsource.ErrNoParent) {
			return Outcome{Kind: NoPriorVersion}
		}
		telemetry.LoggerFromContext(ctx).Error("plandiff: resolve first parent",
			"repo", req.RepoName, "tenant", req.TenantPath, "commit", req.CommitSha, "error", err)
		return Outcome{Kind: CouldNotRender}
	}

	key := cacheKey{repoName: req.RepoName, tenantPath: req.TenantPath, parentSha: parentSha, commitSha: req.CommitSha}

	if cached, ok := e.cache.Get(key); ok {
		return cached
	}

	// group.Do coalesces concurrent Diff calls for the same key into a
	// single computation: only the first caller materializes and parses,
	// every concurrent caller for the same key shares its result -- this is
	// what keeps the parser invocation count at "at most once per key" even
	// under a concurrent burst of identical requests (acceptance
	// criterion 7), not just on the already-cached fast path above.
	v, _, _ := e.group.Do(key.String(), func() (interface{}, error) {
		return e.computeAndCache(ctx, repo, req, key), nil
	})
	return v.(Outcome)
}

// computeAndCache re-checks the cache (closing the race between diff's cache
// check and this call joining the single-flight group -- another goroutine
// may have already populated the cache in between), computes the Outcome on
// a genuine miss, and caches it (including a classified failure) before
// returning.
func (e *Engine) computeAndCache(ctx context.Context, repo PlanRepo, req Request, key cacheKey) Outcome {
	if cached, ok := e.cache.Get(key); ok {
		return cached
	}

	outcome := e.compute(ctx, repo, req, key.parentSha)
	e.cache.Add(key, outcome)
	return outcome
}

// compute materializes and parses both sides of the diff, classifies the
// resource-level delta, and returns the OK Outcome. It never touches the
// cache; computeAndCache owns that.
func (e *Engine) compute(ctx context.Context, repo PlanRepo, req Request, parentSha string) Outcome {
	bounds := gitsource.MaterializeBounds{
		MaxTotalBytes: e.cfg.MaxMaterializedBytes,
		MaxFiles:      e.cfg.MaxMaterializedFiles,
		MaxDepth:      e.cfg.MaxMaterializedDepth,
		MaxTreeNodes:  e.cfg.MaxMaterializedNodes,
	}

	oldResources, failure, ok := e.materializeAndParse(ctx, repo, req, parentSha, bounds, "old")
	if !ok {
		return failure
	}

	newResources, failure, ok := e.materializeAndParse(ctx, repo, req, req.CommitSha, bounds, "new")
	if !ok {
		return failure
	}

	deltas, summary := resourceDelta(oldResources, newResources, forceAttrSet(e.cfg.ForceReplacementAttrs))

	diff := manifestdiff.Diff(manifestdiff.Params{
		Old:             toManifestdiffManifests(oldResources),
		New:             toManifestdiffManifests(newResources),
		MaxUnifiedBytes: e.cfg.MaxUnifiedBytes,
	})

	return Outcome{Kind: OK, Diff: diff, Summary: summary, Resources: deltas}
}
