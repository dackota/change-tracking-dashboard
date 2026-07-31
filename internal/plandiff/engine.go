// Package plandiff is the lazy, credential-free compute engine for a static
// Terraform plan-diff. Given a Terraform-kind Request (repo, stack/module
// directory, commit SHA), Engine.Diff resolves old = the commit's first
// parent tree and new = the commit tree, parses both sides' HCL via a Parser
// into a Resource set, classifies the resource-level delta (added/removed/
// changed, with a replacement-forcing heuristic), renders it through
// manifestdiff, and classifies any unavailability into one of a fixed, safe
// set of Outcome Kinds — never leaking internal git/HCL-parser error detail
// to the caller.
//
// plandiff never executes `terraform plan` or `terraform show -json` and
// never touches cloud credentials or state — its only inputs are HCL bytes
// materialized from git and parsed entirely in-process.
//
// The materialize/cache/bounds orchestration behind that — an in-memory LRU
// cache (keyed by repo/path/parent-SHA/commit-SHA, storing failures too),
// single-flight coalescing, a per-parse timeout, a parse concurrency cap, a
// dedicated materialize timeout and concurrency cap, and the materialization
// ceilings enforced in gitsource — lives in internal/subtree and is shared
// with chartdiff. This package supplies only what is specific to a plan-diff:
// how to parse a directory, how to classify a resource delta, and what this
// package's Outcome Kinds mean (see planDomain in domain.go).
package plandiff

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
)

// instrumentationName scopes the tracer Engine obtains from the injected (or
// default global) TracerProvider — used for every downstream git/parse call
// Engine.Diff's call graph makes (gitsource.first_parent,
// gitsource.materialize_subtree, plandiff.parse).
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
// with NewEngine; it is safe for concurrent use by multiple goroutines.
type Engine struct {
	inner           *subtree.Engine[Resource, Outcome]
	outcomeRecorder OutcomeRecorder
}

// OutcomeRecorder is the seam through which Engine.Diff reports the Kind of
// every Outcome it produces (including cache hits), for the poll-health/
// status surface. *pollstatus.Registry satisfies this directly.
type OutcomeRecorder interface {
	RecordPlanDiffOutcome(kind string)
}

// noopOutcomeRecorder is the default OutcomeRecorder for an Engine built
// without WithOutcomeRecorder, so Diff never needs a nil check.
type noopOutcomeRecorder struct{}

func (noopOutcomeRecorder) RecordPlanDiffOutcome(string) {}

// Option configures optional Engine dependencies (telemetry providers, the
// outcome recorder) at construction time.
type Option func(*options)

type options struct {
	tracerProvider  trace.TracerProvider
	outcomeRecorder OutcomeRecorder
}

// WithTracerProvider wires tp as the source of the tracer Engine.Diff uses
// for its own span and for every downstream git/parse call's child span.
// Tests inject an sdktrace.TracerProvider backed by an in-memory exporter to
// assert on emitted spans without a real OTLP backend.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *options) { o.tracerProvider = tp }
}

// WithOutcomeRecorder wires rec as the destination for every Diff outcome's
// Kind. Without this Option, outcomes are recorded nowhere — Diff's return
// value is unaffected either way.
func WithOutcomeRecorder(rec OutcomeRecorder) Option {
	return func(o *options) { o.outcomeRecorder = rec }
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

	if parser == nil {
		parser = defaultParser{maxBlockDepth: resolved.MaxBlockDepth}
	}

	o := options{outcomeRecorder: noopOutcomeRecorder{}}
	for _, opt := range opts {
		opt(&o)
	}
	var coreOpts []subtree.Option
	if o.tracerProvider != nil {
		coreOpts = append(coreOpts, subtree.WithTracerProvider(o.tracerProvider))
	}

	inner, err := subtree.NewEngine[Resource, Outcome](
		subtree.Config{
			ProduceTimeout:            resolved.ParseTimeout,
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
				Name:                "plandiff",
				ProduceVerb:         "parse",
				ProduceSpanName:     "plandiff.parse",
				InstrumentationName: instrumentationName,
			},
		},
		planDomain{
			parser:          parser,
			maxUnifiedBytes: resolved.MaxUnifiedBytes,
			forceAttrs:      forceAttrSet(resolved.ForceReplacementAttrs),
		},
		coreOpts...,
	)
	if err != nil {
		return nil, err
	}
	return &Engine{inner: inner, outcomeRecorder: o.outcomeRecorder}, nil
}

// Diff computes (or returns the cached) plan-diff Outcome for req against
// repo. It is a total function: for any input, exactly one Outcome Kind is
// returned and Diff never panics — a panic raised anywhere in the engine's
// call graph (first-parent resolution, materialization, or the parse step) is
// recovered and classified. See subtree.Engine.Diff.
//
// Classification:
//   - repo.FirstParent reports gitsource.ErrNoParent (req.CommitSha is a
//     root commit) -> NoPriorVersion.
//   - materialization exceeds a configured bound
//     (gitsource.ErrMaterializeBoundsExceeded), a materialize/parse call
//     exceeds its configured timeout, or a resource body's nested-block
//     recursion exceeds Config.MaxBlockDepth -> ExceededLimits.
//   - any other unclassified failure resolving/materializing/parsing either
//     side (a generic, safe bucket — the specific cause is logged
//     server-side, never returned) -> CouldNotRender.
//   - both sides parse -> OK, with the resource-level Summary/Resources and
//     the manifestdiff.Result.
//
// Every returned Outcome's Kind is reported to the configured
// OutcomeRecorder, including on the cache-hit and single-flight-follower
// fast paths — so the poll-health surface's counts reflect every Diff call
// this Engine has ever served, not just genuine computations.
//
// See planDomain.Classify for the mapping itself, and subtree.Engine.Diff
// for the single-flight cancellation limitation Diff inherits.
func (e *Engine) Diff(ctx context.Context, repo subtree.Repo, req Request) Outcome {
	outcome := e.inner.Diff(ctx, repo, subtree.Request(req))
	e.outcomeRecorder.RecordPlanDiffOutcome(string(outcome.Kind))
	return outcome
}
