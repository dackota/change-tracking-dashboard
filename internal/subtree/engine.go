package subtree

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/lru"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// Request identifies one diff to compute: the tenant subtree directory
// (relative to the repo root) at CommitSha, diffed against its first parent.
// RepoName feeds the cache key, distinguishing two repos that happen to share
// a tenant path.
type Request struct {
	// RepoName identifies the repo CommitSha belongs to.
	RepoName string
	// TenantPath is the tenant subtree directory, relative to the repo root.
	TenantPath string
	// CommitSha is the change commit.
	CommitSha string
}

// Domain is what a diff engine does with the two directories the engine
// materializes for it. Everything else — resolving the first parent, bounded
// materialization into an exclusive temp directory, the cache, single-flight
// coalescing, both concurrency caps, both timeouts, panic containment, and
// span emission — belongs to Engine and is identical for every Domain.
//
// A is the artifact type Produce extracts from a directory; O is the domain's
// own Outcome type, which Engine caches and returns unexamined.
type Domain[A, O any] interface {
	// Produce extracts artifacts from a materialized directory. dir's content
	// is already bounded by the materialize step that populated it; Produce
	// bounds only whatever is internal to itself. It runs on its own
	// goroutine under a timeout and a concurrency cap, with panics recovered,
	// so it need not be interruptible.
	Produce(dir string) ([]A, error)
	// Combine turns both sides' artifacts into the domain's success Outcome.
	// It is called only when both sides produced successfully.
	Combine(old, new []A) O
	// Classify turns an engine Failure into the domain's own Outcome. The
	// domain owns its error vocabulary: for FailureKind ProduceFailed it may
	// inspect Failure.Cause (the error its own Produce returned) and log
	// whatever it does not consider an expected outcome. Engine has already
	// logged every other FailureKind.
	Classify(ctx context.Context, f Failure) O
}

// Labels are the per-domain names that appear in logs, spans, and temp
// directory prefixes. They exist so that moving a domain onto this engine
// changes no observable output: the same log lines, the same span names, the
// same tracer scope as before.
type Labels struct {
	// Name prefixes every log message and names the temp directory pattern
	// ("chartdiff" -> "chartdiff: materialize failed", "chartdiff-*").
	Name string
	// ProduceVerb names the produce step in log messages ("render" ->
	// "chartdiff: render exceeded timeout").
	ProduceVerb string
	// ProduceSpanName is the span wrapping Domain.Produce
	// ("chartrender.render", "plandiff.parse").
	ProduceSpanName string
	// InstrumentationName scopes the tracer obtained from the TracerProvider.
	InstrumentationName string
}

// Config bounds one Engine's resource usage. Every value must already be
// resolved — Engine applies no defaults and performs no validation beyond
// what lru.New itself rejects. Each domain package owns its own public,
// documented Config with its own defaults and validation, and translates it
// into this one at construction time.
type Config struct {
	// ProduceTimeout bounds a single Domain.Produce call.
	ProduceTimeout time.Duration
	// ConcurrencyCap bounds how many Produce calls may run concurrently.
	ConcurrencyCap int
	// CacheEntries bounds the in-memory LRU cache's entry count.
	CacheEntries int
	// MaterializeTimeout bounds a single Repo.MaterializeSubtreeBounded call.
	MaterializeTimeout time.Duration
	// MaterializeConcurrencyCap bounds how many materializations may run
	// concurrently, independently of ConcurrencyCap. A dedicated cap — rather
	// than sharing one semaphore with Produce — is deliberate: materialize is
	// a disk/CPU tree walk and Produce is CPU-bound work over the result, two
	// different resource profiles with their own natural ceilings. Sharing
	// one semaphore would let a burst of slow materializations starve Produce
	// slots, or the reverse.
	MaterializeConcurrencyCap int
	// Materialize bounds a single subtree materialization's disk footprint.
	Materialize gitsource.MaterializeBounds
	// Labels name this engine in logs, spans, and temp directories.
	Labels Labels
}

// Engine computes, caches, and coalesces diffs of a tenant subtree between a
// commit and its first parent. Construct one with NewEngine; it is safe for
// concurrent use by multiple goroutines (its cache, semaphores, and
// single-flight group are all internally synchronized).
type Engine[A, O any] struct {
	cfg    Config
	domain Domain[A, O]
	cache  *lru.Cache[cacheKey, O]
	// sem bounds concurrent Produce invocations (Config.ConcurrencyCap).
	sem chan struct{}
	// materializeSem bounds concurrent Repo.MaterializeSubtreeBounded
	// invocations (Config.MaterializeConcurrencyCap) — see that field's doc
	// for why it is not shared with sem.
	materializeSem chan struct{}
	group          singleflight.Group
	// tracer wraps every downstream git/produce call Diff's call graph makes
	// in its own child span (telemetry.WithSpan) — see WithTracerProvider.
	tracer trace.Tracer
}

// Option configures optional Engine dependencies (telemetry providers) at
// construction time.
type Option func(cfg *engineOptions)

type engineOptions struct {
	tracerProvider trace.TracerProvider
}

// WithTracerProvider wires tp as the source of the tracer Engine.Diff uses
// for every downstream git/produce call's child span. Tests inject an
// sdktrace.TracerProvider backed by an in-memory exporter to assert on
// emitted spans without a real OTLP backend.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *engineOptions) { o.tracerProvider = tp }
}

// NewEngine constructs an Engine over domain, bounded by cfg. cfg's values
// must already be resolved by the calling domain package. Without a
// WithTracerProvider Option, tracing defaults to the ambient global OTel
// TracerProvider (a safe no-op until telemetry.Init registers the real one).
func NewEngine[A, O any](cfg Config, domain Domain[A, O], opts ...Option) (*Engine[A, O], error) {
	cache, err := lru.New[cacheKey, O](cfg.CacheEntries)
	if err != nil {
		return nil, fmt.Errorf("%s: create cache: %w", cfg.Labels.Name, err)
	}

	var resolved engineOptions
	for _, opt := range opts {
		opt(&resolved)
	}
	tp := resolved.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	return &Engine[A, O]{
		cfg:            cfg,
		domain:         domain,
		cache:          cache,
		sem:            make(chan struct{}, cfg.ConcurrencyCap),
		materializeSem: make(chan struct{}, cfg.MaterializeConcurrencyCap),
		tracer:         tp.Tracer(cfg.Labels.InstrumentationName),
	}, nil
}

// Diff computes (or returns the cached) Outcome for req against repo. It is a
// total function: for any input, exactly one Outcome is returned and Diff
// never panics — a panic in either the materialize or the produce step is
// recovered on the goroutine that raised it and folded into a Failure.
//
// Known limitation: e.group.Do coalesces concurrent Diff calls for the same
// key onto a single computation, which runs under only the *leader's* ctx
// (the caller whose call actually triggered computeAndCache). A follower call
// coalesced onto that in-flight computation waits for it to finish regardless
// of its own ctx being cancelled — singleflight has no per-caller
// cancellation. This is inherent to singleflight, pre-existing, and not
// addressed here; it does not affect the leader, and a follower is still
// bounded by the leader's own timeouts and bounds checks.
func (e *Engine[A, O]) Diff(ctx context.Context, repo Repo, req Request) O {
	var parentSha string
	err := telemetry.WithSpan(ctx, e.tracer, "gitsource.first_parent", func(context.Context) error {
		v, err := repo.FirstParent(req.CommitSha)
		parentSha = v
		return err
	})
	if err != nil {
		if errors.Is(err, gitsource.ErrNoParent) {
			return e.domain.Classify(ctx, Failure{Kind: NoPriorVersion, Req: req})
		}
		telemetry.LoggerFromContext(ctx).Error(e.cfg.Labels.Name+": resolve first parent",
			"repo", req.RepoName, "tenant", req.TenantPath, "commit", req.CommitSha, "error", err)
		return e.domain.Classify(ctx, Failure{Kind: Internal, Req: req})
	}

	key := cacheKey{repoName: req.RepoName, tenantPath: req.TenantPath, parentSha: parentSha, commitSha: req.CommitSha}

	if cached, ok := e.cache.Get(key); ok {
		return cached
	}

	// group.Do coalesces concurrent Diff calls for the same key into a single
	// computation: only the first caller materializes and produces, every
	// concurrent caller for the same key shares its result. This is what
	// keeps the Produce invocation count at "at most once per key" even under
	// a concurrent burst of identical requests, not just on the
	// already-cached fast path above.
	v, _, _ := e.group.Do(key.String(), func() (interface{}, error) {
		return e.computeAndCache(ctx, repo, req, key), nil
	})
	return v.(O)
}

// computeAndCache re-checks the cache (closing the race between Diff's cache
// check and this call joining the single-flight group — another goroutine may
// have already populated the cache in between), computes the Outcome on a
// genuine miss, and caches it (including a classified failure) before
// returning.
func (e *Engine[A, O]) computeAndCache(ctx context.Context, repo Repo, req Request, key cacheKey) O {
	if cached, ok := e.cache.Get(key); ok {
		return cached
	}

	outcome := e.compute(ctx, repo, req, key.parentSha)
	e.cache.Add(key, outcome)
	return outcome
}

// compute materializes and produces both sides of the diff and returns the
// domain's Outcome. It never touches the cache; computeAndCache owns that.
func (e *Engine[A, O]) compute(ctx context.Context, repo Repo, req Request, parentSha string) O {
	old, failure, ok := e.materializeAndProduce(ctx, repo, req, parentSha, "old")
	if !ok {
		return e.domain.Classify(ctx, failure)
	}

	new, failure, ok := e.materializeAndProduce(ctx, repo, req, req.CommitSha, "new")
	if !ok {
		return e.domain.Classify(ctx, failure)
	}

	return e.domain.Combine(old, new)
}
