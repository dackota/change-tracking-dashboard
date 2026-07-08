// Package poller orchestrates a single polling cycle for one Tracker:
// it asks the Git source for new commits since the high-water mark, runs
// Extractor → Differ across consecutive file snapshots, attaches facets,
// and persists resulting Changes + the new high-water mark via the Store.
//
// The Poller is a thin coordinator — it delegates all logic to the pure modules
// (extractor, differ, facet) and the I/O edges (gitsource, store).
//
// On first run (HWM empty), the walk is bounded to the BackfillDays window
// configured on the Tracker. An injectable clock (WithNow) enables deterministic
// testing against fixture repos with fixed commit dates.
//
// Observability: Poll is the poll-cycle seam the observability standard
// instruments (see internal/telemetry). Every call emits the generic RED
// signal under the single, bounded-cardinality operation label "poll" —
// never the tracker's repo or file path, which would blow up metric
// cardinality across many tracked repos. Each downstream git/store call
// pollFile makes is wrapped in its own child span (telemetry.WithSpan), and
// every log line emitted during a poll cycle is structured JSON correlated
// to that cycle's trace/span ID. WithTracerProvider/WithMeterProvider/
// WithLogger wire in the process-wide SDK from cmd/dashboard/main.go; a
// Poller built without them (as every pre-existing test does) still works
// exactly as before — the OTel API's default providers are safe no-ops.
package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/differ"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/extractor"
	"github.com/dackota/change-tracking-dashboard/internal/facet"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/issueref"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// pollOperation is the single, constant RED operation label recorded for
// every poll cycle — deliberately not the tracker's repo/file-glob, which
// would make the metric's cardinality grow with every tracked repo.
const pollOperation = "poll"

// instrumentationName scopes the tracer/meter this package obtains from the
// injected (or default global) providers.
const instrumentationName = "github.com/dackota/change-tracking-dashboard/internal/poller"

// diffFields dispatches to DiffKeyed or DiffScalar based on whether either
// TrackedField is a keyed map result. If either old or new is keyed, both are
// treated as keyed (a nil Map is equivalent to an empty map for keyed diffing).
// This means the poller does not need explicit kind configuration on the Tracker
// — the extractor's output type determines the diff path automatically.
func diffFields(p differ.ScalarParams, old, new domain.TrackedField) []domain.Change {
	if old.IsKeyed() || new.IsKeyed() {
		return differ.DiffKeyed(p, old, new)
	}
	return differ.DiffScalar(p, old, new)
}

// ExtractFailureRecorder is the seam through which Poll reports a
// FieldExtractor.Extract failure (e.g. an unparseable HCL file) to the
// poll-health/status surface, tagged by engine (e.g. "hcl", "jq") so one
// engine's structural parse failures are never conflated with another's
// evaluation failures. pollstatus.Registry satisfies this interface
// directly; tests may substitute a fake without importing that package,
// mirroring scheduler.StatusRecorder's role for the scheduler.
type ExtractFailureRecorder interface {
	RecordExtractFailure(engine string)
}

// noopExtractFailureRecorder is the default ExtractFailureRecorder for a
// Poller built without WithExtractFailureRecorder, so pollFile never needs
// to nil-check it.
type noopExtractFailureRecorder struct{}

func (noopExtractFailureRecorder) RecordExtractFailure(string) {}

// Poller wires the git source and store together to run polling cycles.
type Poller struct {
	src *gitsource.Source
	st  *store.Store
	// now returns the current wall time. Defaults to time.Now; tests may inject
	// a fixed clock to make the backfill window deterministic.
	now func() time.Time

	tracer          trace.Tracer
	red             *telemetry.REDMetrics
	logger          *slog.Logger
	extractFailures ExtractFailureRecorder
}

// Option configures optional Poller dependencies (telemetry providers,
// logger) at construction time. See WithTracerProvider, WithMeterProvider,
// WithLogger.
type Option func(*Poller)

// WithTracerProvider wires tp as the source of the tracer Poll uses for its
// own span and for every downstream git/store call's child span. Tests
// inject an sdktrace.TracerProvider backed by an in-memory exporter to
// assert on emitted spans without a real OTLP backend.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(p *Poller) {
		p.tracer = tp.Tracer(instrumentationName)
	}
}

// WithMeterProvider wires mp as the source of the poll cycle's RED metrics.
// Tests inject an sdkmetric.MeterProvider backed by a ManualReader to assert
// on emitted signals without a real OTLP backend.
//
// The RED instruments' names are static, package-controlled constants; a
// construction failure here is a programming error, not a runtime
// condition, so it panics rather than threading an error return through
// every option (mirroring this codebase's existing template.Must
// convention for the same class of "can't happen in production" failure).
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(p *Poller) {
		red, err := telemetry.NewREDMetrics(mp, instrumentationName)
		if err != nil {
			panic(fmt.Sprintf("poller: create RED metrics: %v", err))
		}
		p.red = red
	}
}

// WithLogger wires logger as the base structured logger Poll correlates to
// its own trace/span ID for every log line emitted during a poll cycle.
func WithLogger(logger *slog.Logger) Option {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithExtractFailureRecorder wires rec as the destination for per-engine
// extract-failure counts (e.g. HCL structural parse failures) recorded
// during Poll — see ExtractFailureRecorder. Without this Option, failures
// are still logged and returned as errors exactly as before; only the
// poll-health/status surface's count is skipped.
func WithExtractFailureRecorder(rec ExtractFailureRecorder) Option {
	return func(p *Poller) {
		p.extractFailures = rec
	}
}

// New returns a Poller wired to the given source and store. Without any
// Option, telemetry defaults to the ambient global OTel providers (a safe
// no-op until cmd/dashboard/main.go calls telemetry.Init) and a
// package-default structured JSON logger — Poll behaves identically to
// before this package was instrumented.
func New(src *gitsource.Source, st *store.Store, opts ...Option) *Poller {
	p := &Poller{
		src:             src,
		st:              st,
		now:             time.Now,
		tracer:          otel.GetTracerProvider().Tracer(instrumentationName),
		red:             mustNoopREDMetrics(),
		logger:          telemetry.LoggerFromContext(context.Background()),
		extractFailures: noopExtractFailureRecorder{},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// mustNoopREDMetrics builds RED instruments against the ambient global
// MeterProvider default (a real, harmless no-op until Init registers a real
// one) so New never needs to special-case a nil *REDMetrics at every Poll
// call site.
func mustNoopREDMetrics() *telemetry.REDMetrics {
	red, err := telemetry.NewREDMetrics(otel.GetMeterProvider(), instrumentationName)
	if err != nil {
		panic(fmt.Sprintf("poller: create default RED metrics: %v", err))
	}
	return red
}

// WithNow returns a copy of the Poller with a custom clock function. It is
// intended for tests that need a deterministic reference point for the backfill
// window calculation. Every other field — including any telemetry wired in
// via New's Options — carries over unchanged.
func (p *Poller) WithNow(fn func() time.Time) *Poller {
	return &Poller{
		src: p.src, st: p.st, now: fn,
		tracer: p.tracer, red: p.red, logger: p.logger,
		extractFailures: p.extractFailures,
	}
}

// globMetaChars are the path.Match wildcard characters. A FileGlob containing
// any of these is fanned out across the repo tree; one with none of them is a
// literal path and is walked directly (no enumeration), preserving prior
// behavior exactly.
const globMetaChars = "*?["

// isGlob reports whether pattern contains any path.Match wildcard metacharacter.
func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, globMetaChars)
}

// Poll runs one polling cycle for the given Tracker:
//  1. Resolve Tracker.FileGlob to the set of concrete file paths to walk: a
//     literal path resolves to itself; a wildcard glob is expanded against the
//     repo's HEAD tree via gitsource.MatchingFiles.
//  2. For each resolved file path, independently: read its own high-water-mark
//     (keyed by repo+path so fanned-out files never share a cursor), walk its
//     commit history, and run Extractor → Differ → facet attachment exactly as
//     for a single tracked file. On first run (HWM empty) the walk is bounded
//     by the backfill window (Tracker.BackfillDays days before now).
//  3. Persist all resulting Changes and each file's high-water mark.
func (p *Poller) Poll(t domain.Tracker) error {
	ctx, span := p.tracer.Start(context.Background(), "poller.poll")
	defer span.End()

	start := time.Now()
	logger := telemetry.FromContext(ctx, p.logger)
	// Store the poll-scoped, trace-correlated logger on ctx (mirroring
	// telemetry.Middleware's request-scoped equivalent) so downstream
	// packages that only receive a ctx — not an explicit logger parameter,
	// e.g. gitsource.WalkCommits — can retrieve the same correlated logger
	// via telemetry.LoggerFromContext instead of falling back to the
	// uncorrelated package default.
	ctx = telemetry.ContextWithLogger(ctx, logger)

	err := p.pollTracker(ctx, logger, t)

	p.red.Record(ctx, pollOperation, err, time.Since(start))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		logger.Error("poller: poll cycle failed",
			slog.String("repo", t.Repo),
			slog.String("fileGlob", t.FileGlob),
			slog.Any("error", err))
	}
	return err
}

// pollTracker holds Poll's original, unchanged business logic: resolve the
// file glob, then run pollFile for each resolved path. Split out from Poll
// purely so Poll can wrap the whole cycle in a span/RED signal without
// mixing that concern into the tracker-polling logic itself.
func (p *Poller) pollTracker(ctx context.Context, logger *slog.Logger, t domain.Tracker) error {
	engine := extractor.InferEngine(t.Engine, t.FileGlob)
	ex, err := extractor.Select(engine, t.ExtractorExpr)
	if err != nil {
		return fmt.Errorf("poller: select extractor (engine=%q, expr=%q): %w", engine, t.ExtractorExpr, err)
	}

	fe, err := facet.NewExtractor(t.FacetPattern)
	if err != nil {
		return fmt.Errorf("poller: compile facet pattern %q: %w", t.FacetPattern, err)
	}

	filePaths, err := p.resolveFilePaths(ctx, t.FileGlob)
	if err != nil {
		return fmt.Errorf("poller: resolve file glob %q: %w", t.FileGlob, err)
	}

	// A failure on one resolved file (e.g. an extractor expression that throws
	// on that file's shape) must not drop every other file in the same tracker
	// cycle. Collect per-file errors and continue; the changes from the files
	// that DID parse are already persisted. errors.Join returns nil when the
	// slice is empty, so a fully clean cycle still returns nil.
	var errs []error
	for _, filePath := range filePaths {
		if err := p.pollFile(ctx, logger, t, filePath, engine, ex, fe); err != nil {
			errs = append(errs, fmt.Errorf("file %q: %w", filePath, err))
		}
	}

	return errors.Join(errs...)
}

// resolveFilePaths expands glob into the concrete file paths to walk. A
// literal path (no wildcard metacharacters) resolves to itself unconditionally
// — even if the file doesn't exist at HEAD — preserving the pre-fan-out
// behavior where WalkCommits is simply attempted against the literal path. A
// wildcard glob is expanded against the repo's HEAD tree.
func (p *Poller) resolveFilePaths(ctx context.Context, glob string) ([]string, error) {
	if !isGlob(glob) {
		return []string{glob}, nil
	}
	var paths []string
	err := telemetry.WithSpan(ctx, p.tracer, "gitsource.matching_files", func(context.Context) error {
		var err error
		paths, err = p.src.MatchingFiles(glob)
		return err
	})
	return paths, err
}

// pollFile runs one polling cycle for a single concrete file path: read its
// own HWM, walk its commit history (bounded by the backfill window on first
// run), diff consecutive snapshots, attach facets from this file's own path,
// and persist Changes plus the file's new HWM. Every downstream git/store
// call is wrapped in its own child span (telemetry.WithSpan): a failure is
// both logged (with repo/path context — safe here, since logs, unlike
// metric labels, are not cardinality-bounded) and returned unchanged, so
// existing callers and tests observe identical error behavior to before.
//
// ex is typed as the extractor.FieldExtractor interface (not the concrete
// gojq-based *extractor.Extractor) so an alternate backend — e.g. the HCL
// extractor — can be substituted without pollFile changing at all. engine is
// the resolved engine name (see extractor.InferEngine) tagging any Extract
// failure reported to the ExtractFailureRecorder.
func (p *Poller) pollFile(ctx context.Context, logger *slog.Logger, t domain.Tracker, filePath, engine string, ex extractor.FieldExtractor, fe *facet.Extractor) error {
	var hwm string
	err := telemetry.WithSpan(ctx, p.tracer, "store.get_high_water_mark", func(context.Context) error {
		v, err := p.st.GetHighWaterMark(t.Repo, filePath)
		hwm = v
		return err
	})
	if err != nil {
		logger.Error("poller: get high-water mark failed", slog.String("repo", t.Repo), slog.String("filePath", filePath), slog.Any("error", err))
		return fmt.Errorf("poller: get HWM for %q/%q: %w", t.Repo, filePath, err)
	}

	// On first run, bound the walk to the configured backfill window.
	// On incremental runs the HWM already provides the boundary; no time bound.
	var notBefore time.Time
	if hwm == "" && t.BackfillDays >= 0 {
		notBefore = p.now().Add(-time.Duration(t.BackfillDays) * 24 * time.Hour)
	}

	var snapshots []domain.CommitSnapshot
	err = telemetry.WithSpan(ctx, p.tracer, "gitsource.walk_commits", func(ctx context.Context) error {
		v, err := p.src.WalkCommits(ctx, filePath, hwm, notBefore)
		snapshots = v
		return err
	})
	if err != nil {
		logger.Error("poller: walk commits failed", slog.String("repo", t.Repo), slog.String("filePath", filePath), slog.Any("error", err))
		return fmt.Errorf("poller: walk commits for %q: %w", filePath, err)
	}

	if len(snapshots) == 0 {
		return nil // nothing new since last poll
	}

	// We need a "before" snapshot to diff against. When there is no HWM yet
	// (first run), we treat the state before the oldest snapshot as absent.
	var prevField domain.TrackedField
	if hwm == "" && len(snapshots) > 0 {
		// Extract state of the very first snapshot as the initial "old" value.
		// Then walk pairs starting from index 1 using the first as old.
		// This means: if there's only one snapshot, we produce an "added" Change.
		prevField, err = p.extractField(logger, engine, t, filePath, ex, snapshots[0].CommitSha, snapshots[0].Content)
		if err != nil {
			return fmt.Errorf("poller: extract (initial): %w", err)
		}
		if len(snapshots) == 1 {
			// Only one commit ever — treat absent→first commit as "added".
			facets := fe.ExtractFacets(snapshots[0].FilePath)
			params := differ.ScalarParams{
				Repo:        t.Repo,
				FilePath:    snapshots[0].FilePath,
				Field:       t.Field,
				CommitSha:   snapshots[0].CommitSha,
				Author:      snapshots[0].Author,
				CommittedAt: snapshots[0].CommittedAt,
				Facets:      facets,
				IssueRefs:   issueref.Parse(snapshots[0].Message),
			}
			changes := diffFields(params, domain.TrackedField{Present: false}, prevField)
			for _, c := range changes {
				if err := p.saveChange(ctx, logger, t, filePath, c); err != nil {
					return err
				}
			}
			return p.setHighWaterMark(ctx, logger, t, filePath, snapshots[0].CommitSha)
		}
		snapshots = snapshots[1:]
	} else if hwm != "" {
		// There IS a previous snapshot already processed. We need the file
		// state at the HWM commit to compute the diff for the first new commit.
		// This lookup MUST be unbounded (zero notBefore) so we can find an HWM
		// commit that may predate the backfill window.
		var hwmSnaps []domain.CommitSnapshot
		err = telemetry.WithSpan(ctx, p.tracer, "gitsource.walk_commits", func(ctx context.Context) error {
			v, err := p.src.WalkCommits(ctx, filePath, "", time.Time{})
			hwmSnaps = v
			return err
		})
		if err != nil {
			logger.Error("poller: reload all commits for HWM lookup failed", slog.String("repo", t.Repo), slog.String("filePath", filePath), slog.Any("error", err))
			return fmt.Errorf("poller: reload all commits for HWM lookup: %w", err)
		}
		for _, snap := range hwmSnaps {
			if snap.CommitSha == hwm {
				prevField, err = p.extractField(logger, engine, t, filePath, ex, snap.CommitSha, snap.Content)
				if err != nil {
					return fmt.Errorf("poller: extract HWM content: %w", err)
				}
				break
			}
		}
	}

	var lastSha string
	for _, snap := range snapshots {
		newField, err := p.extractField(logger, engine, t, filePath, ex, snap.CommitSha, snap.Content)
		if err != nil {
			return fmt.Errorf("poller: extract at %s: %w", snap.CommitSha, err)
		}

		facets := fe.ExtractFacets(snap.FilePath)
		params := differ.ScalarParams{
			Repo:        t.Repo,
			FilePath:    snap.FilePath,
			Field:       t.Field,
			CommitSha:   snap.CommitSha,
			Author:      snap.Author,
			CommittedAt: snap.CommittedAt,
			Facets:      facets,
			IssueRefs:   issueref.Parse(snap.Message),
		}

		changes := diffFields(params, prevField, newField)
		for _, c := range changes {
			if err := p.saveChange(ctx, logger, t, filePath, c); err != nil {
				return err
			}
		}

		prevField = newField
		lastSha = snap.CommitSha
	}

	if lastSha != "" {
		return p.setHighWaterMark(ctx, logger, t, filePath, lastSha)
	}

	return nil
}

// extractField wraps a single ex.Extract call, logging and reporting a
// failure to the ExtractFailureRecorder (tagged with engine, e.g. "hcl") in
// one place — every one of pollFile's three extraction sites (initial
// baseline, HWM-content lookup, and the main per-commit loop) shares this so
// a malformed or unparseable file is consistently logged and counted on the
// poll-health/status surface no matter which site hits it.
func (p *Poller) extractField(logger *slog.Logger, engine string, t domain.Tracker, filePath string, ex extractor.FieldExtractor, commitSha string, content []byte) (domain.TrackedField, error) {
	field, err := ex.Extract(content)
	if err != nil {
		logger.Error("poller: extract failed",
			slog.String("repo", t.Repo),
			slog.String("filePath", filePath),
			slog.String("commitSha", commitSha),
			slog.String("engine", engine),
			slog.Any("error", err))
		p.extractFailures.RecordExtractFailure(engine)
		return domain.TrackedField{}, fmt.Errorf("engine=%q: %w", engine, err)
	}
	return field, nil
}

// saveChange wraps one store.SaveChange call in its own span, logging and
// wrapping the error with the same message the pre-instrumentation code
// used at each of its two call sites.
func (p *Poller) saveChange(ctx context.Context, logger *slog.Logger, t domain.Tracker, filePath string, c domain.Change) error {
	err := telemetry.WithSpan(ctx, p.tracer, "store.save_change", func(context.Context) error {
		return p.st.SaveChange(c)
	})
	if err != nil {
		logger.Error("poller: save change failed", slog.String("repo", t.Repo), slog.String("filePath", filePath), slog.Any("error", err))
		return fmt.Errorf("poller: save change: %w", err)
	}
	return nil
}

// setHighWaterMark wraps one store.SetHighWaterMark call in its own span.
func (p *Poller) setHighWaterMark(ctx context.Context, logger *slog.Logger, t domain.Tracker, filePath, sha string) error {
	err := telemetry.WithSpan(ctx, p.tracer, "store.set_high_water_mark", func(context.Context) error {
		return p.st.SetHighWaterMark(t.Repo, filePath, sha)
	})
	if err != nil {
		logger.Error("poller: set high-water mark failed", slog.String("repo", t.Repo), slog.String("filePath", filePath), slog.Any("error", err))
		return fmt.Errorf("poller: set HWM: %w", err)
	}
	return nil
}
