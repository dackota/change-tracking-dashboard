// Package poller orchestrates a single polling cycle for a group of Trackers
// sharing one (Repo, FileGlob): it asks the Git source for new commits since
// the high-water mark, runs Extractor → Differ across consecutive file
// snapshots, attaches facets, and persists resulting Changes + the new
// high-water mark via the Store.
//
// The unit of work is a tracker GROUP, not a single Tracker (see PollGroup).
// Config flattens to one Tracker per (repo × file-glob × field), and all the
// fields watched in one file set derive from the same commit histories — so
// each matched file's history is walked once for the whole group and every
// field extracted from those shared snapshots. High-water marks remain strictly
// per-field, so fields keep independent cursors. Poll is the one-tracker case.
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
// pollFileGroup makes is wrapped in its own child span (telemetry.WithSpan), and
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

// commitSubject returns the first line of a commit message (see #85), with
// surrounding whitespace trimmed. An empty message yields an empty subject.
func commitSubject(message string) string {
	subject, _, _ := strings.Cut(message, "\n")
	return strings.TrimSpace(subject)
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
// Poller built without WithExtractFailureRecorder, so the extract path never needs
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

// Poll runs one polling cycle for a single Tracker. It is exactly the
// one-tracker case of PollGroup — see that method for the cycle's steps.
func (p *Poller) Poll(t domain.Tracker) error {
	return p.PollGroup([]domain.Tracker{t})[0]
}

// PollGroup runs one polling cycle for a group of Trackers that all share the
// same (Repo, FileGlob) and differ only in Field/ExtractorExpr — the shape
// config produces when several fields are watched in the same set of files:
//
//  1. Resolve the shared FileGlob to the set of concrete file paths to walk: a
//     literal path resolves to itself; a wildcard glob is expanded against the
//     repo's HEAD tree via gitsource.MatchingFiles.
//  2. For each resolved file path, walk its commit history exactly ONCE and
//     run Extractor → Differ → facet attachment for every field in the group
//     against those shared snapshots.
//  3. Persist all resulting Changes and each (file, field)'s high-water mark.
//
// Walking once per file rather than once per (file, field) is the whole point
// of the group form (#137): the fields in a group derive from identical
// histories, and go-git's file-filtered log has to walk the commit graph and
// diff trees per commit, so repeating it per field multiplied the poller's CPU
// by the field count.
//
// High-water marks stay strictly per-field (#109): every field keeps its own
// cursor, so a field added to an already-caught-up group still runs its own
// full backfill. The single shared walk therefore starts from the OLDEST
// boundary any field in the group still needs, and each field then replays
// only its own slice of that history.
//
// The returned slice holds one error per input tracker, index-aligned with ts,
// nil where that tracker's poll succeeded. A failure isolated to one tracker
// (a bad extractor expression) or one file never drops its siblings' work.
//
// All trackers in ts must share one (Repo, FileGlob): the Repo decides which
// git source the caller opened, and the FileGlob decides the file set walked.
// A mismatched group is a caller bug and is reported as an error against
// every member rather than silently polling the wrong repo.
func (p *Poller) PollGroup(ts []domain.Tracker) []error {
	if len(ts) == 0 {
		return nil
	}

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

	errs := p.pollGroup(ctx, logger, ts)

	// One RED signal per tracker, not per group call, so the request/error
	// counters keep counting what the scheduler schedules — tracker polls.
	// The duration is necessarily the whole group's elapsed time: the walk
	// work is genuinely shared now, so there is no per-field share to report.
	elapsed := time.Since(start)
	for i, t := range ts {
		p.red.Record(ctx, pollOperation, errs[i], elapsed)
		if errs[i] == nil {
			continue
		}
		span.RecordError(errs[i])
		span.SetStatus(codes.Error, errs[i].Error())
		logger.Error("poller: poll cycle failed",
			slog.String("repo", t.Repo),
			slog.String("fileGlob", t.FileGlob),
			slog.String("field", t.Field),
			slog.Any("error", errs[i]))
	}
	return errs
}

// groupMember is one tracker in a group paired with the extractor and facet
// machinery compiled for it. idx is the tracker's position in PollGroup's
// input, so a failure is reported against the right tracker.
type groupMember struct {
	idx     int
	tracker domain.Tracker
	engine  string
	ex      extractor.FieldExtractor
	fe      *facet.Extractor
}

// pollGroup holds PollGroup's business logic: compile each member's
// extractor/facet pattern, resolve the shared file glob, then walk each
// resolved file once and replay every member against that one history. Split
// out from PollGroup purely so PollGroup can wrap the whole cycle in a
// span/RED signal without mixing that concern into the polling logic.
func (p *Poller) pollGroup(ctx context.Context, logger *slog.Logger, ts []domain.Tracker) []error {
	errs := make([]error, len(ts))

	if err := checkGroupShared(ts); err != nil {
		for i := range errs {
			errs[i] = err
		}
		return errs
	}

	// A member whose own extractor expression or facet pattern is invalid
	// fails alone; the rest of the group still polls.
	members := make([]groupMember, 0, len(ts))
	for i, t := range ts {
		engine := extractor.InferEngine(t.Engine, t.FileGlob)
		ex, err := extractor.Select(engine, t.ExtractorExpr)
		if err != nil {
			errs[i] = fmt.Errorf("poller: select extractor (engine=%q, expr=%q): %w", engine, t.ExtractorExpr, err)
			continue
		}
		fe, err := facet.NewExtractor(t.FacetPattern)
		if err != nil {
			errs[i] = fmt.Errorf("poller: compile facet pattern %q: %w", t.FacetPattern, err)
			continue
		}
		members = append(members, groupMember{idx: i, tracker: t, engine: engine, ex: ex, fe: fe})
	}
	if len(members) == 0 {
		return errs
	}

	glob := ts[0].FileGlob
	filePaths, err := p.resolveFilePaths(ctx, glob)
	if err != nil {
		err = fmt.Errorf("poller: resolve file glob %q: %w", glob, err)
		for _, m := range members {
			errs[m.idx] = err
		}
		return errs
	}

	// A failure on one resolved file (e.g. an extractor expression that throws
	// on that file's shape) must not drop every other file in the same cycle.
	// Per-file errors accumulate onto the owning tracker and the loop
	// continues; the changes from the files that DID parse are already
	// persisted.
	for _, filePath := range filePaths {
		p.pollFileGroup(ctx, logger, filePath, members, errs)
	}

	return errs
}

// checkGroupShared enforces PollGroup's precondition that every tracker in
// the group shares one (Repo, FileGlob).
func checkGroupShared(ts []domain.Tracker) error {
	for _, t := range ts[1:] {
		if t.Repo != ts[0].Repo || t.FileGlob != ts[0].FileGlob {
			return fmt.Errorf("poller: tracker group spans more than one (repo, file-glob): %q/%q and %q/%q",
				ts[0].Repo, ts[0].FileGlob, t.Repo, t.FileGlob)
		}
	}
	return nil
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

// fileHistory is one file's walked commit history, oldest first — the single
// shared walk result every field in a tracker group replays against.
type fileHistory []domain.CommitSnapshot

// since returns the commits strictly after sha, plus the snapshot at sha.
// When sha is absent from the history (e.g. the cursor's commit was rewritten
// away) the whole history is returned with found=false, matching
// gitsource.WalkCommits — which walks to the root when it never meets its stop
// commit, leaving the caller with no baseline to diff from.
func (h fileHistory) since(sha string) (rest fileHistory, at domain.CommitSnapshot, found bool) {
	for i, snap := range h {
		if snap.CommitSha == sha {
			return h[i+1:], snap, true
		}
	}
	return h, domain.CommitSnapshot{}, false
}

// notBefore returns the commits from HEAD back to — but excluding — the first
// commit older than bound. It stops at the boundary rather than filtering
// across it, reproducing gitsource.WalkCommits' own break-on-boundary
// semantics exactly, so a non-monotonic author date truncates identically
// whether the bound was applied during the walk or afterwards here.
func (h fileHistory) notBefore(bound time.Time) fileHistory {
	if bound.IsZero() {
		return h
	}
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].CommittedAt.Before(bound) {
			return h[i+1:]
		}
	}
	return h
}

// pollFileGroup runs one polling cycle for a single concrete file path across
// every field in the group: read each field's own HWM, walk the file's commit
// history ONCE (bounded by the oldest boundary any field still needs), then
// replay each field over its own slice of that history. Per-field failures
// accumulate onto errs at the owning tracker's index; a failure never stops
// the group's other fields.
func (p *Poller) pollFileGroup(ctx context.Context, logger *slog.Logger, filePath string, members []groupMember, errs []error) {
	// Read every field's cursor first: the set of cursors decides how far back
	// the single shared walk has to reach.
	hwms := make([]string, len(members))
	live := make([]int, 0, len(members))
	for i, m := range members {
		hwm, err := p.getHighWaterMark(ctx, logger, m.tracker, filePath)
		if err != nil {
			errs[m.idx] = errors.Join(errs[m.idx], fmt.Errorf("file %q: %w", filePath, err))
			continue
		}
		hwms[i] = hwm
		live = append(live, i)
	}
	if len(live) == 0 {
		return
	}

	history, err := p.walkHistory(ctx, logger, members[live[0]].tracker, filePath, p.groupWalkBound(members, hwms, live))
	if err != nil {
		for _, i := range live {
			errs[members[i].idx] = errors.Join(errs[members[i].idx], fmt.Errorf("file %q: %w", filePath, err))
		}
		return
	}
	if len(history) == 0 {
		return // no history in range — nothing for any field to do
	}

	for _, i := range live {
		m := members[i]
		if err := p.pollFieldHistory(ctx, logger, m, filePath, hwms[i], history); err != nil {
			errs[m.idx] = errors.Join(errs[m.idx], fmt.Errorf("file %q: %w", filePath, err))
		}
	}
}

// groupWalkBound is the lower time bound for the group's single shared walk:
// the oldest boundary any live field still needs. A field that already has a
// cursor needs the unbounded history — its cursor commit may predate every
// configured backfill window — so one such field drops the bound entirely.
func (p *Poller) groupWalkBound(members []groupMember, hwms []string, live []int) time.Time {
	var oldest time.Time
	for _, i := range live {
		if hwms[i] != "" {
			return time.Time{}
		}
		bound := p.backfillBound(members[i].tracker)
		if bound.IsZero() {
			return time.Time{}
		}
		if oldest.IsZero() || bound.Before(oldest) {
			oldest = bound
		}
	}
	return oldest
}

// backfillBound is t's first-run walk boundary: BackfillDays before now. A
// negative BackfillDays means no bound at all (the zero time).
func (p *Poller) backfillBound(t domain.Tracker) time.Time {
	if t.BackfillDays < 0 {
		return time.Time{}
	}
	return p.now().Add(-time.Duration(t.BackfillDays) * 24 * time.Hour)
}

// getHighWaterMark wraps one store.GetHighWaterMark call in its own span.
func (p *Poller) getHighWaterMark(ctx context.Context, logger *slog.Logger, t domain.Tracker, filePath string) (string, error) {
	var hwm string
	err := telemetry.WithSpan(ctx, p.tracer, "store.get_high_water_mark", func(context.Context) error {
		v, err := p.st.GetHighWaterMark(t.Repo, filePath, t.Field)
		hwm = v
		return err
	})
	if err != nil {
		logger.Error("poller: get high-water mark failed", slog.String("repo", t.Repo), slog.String("filePath", filePath), slog.Any("error", err))
		return "", fmt.Errorf("poller: get HWM for %q/%q: %w", t.Repo, filePath, err)
	}
	return hwm, nil
}

// walkHistory wraps the group's one gitsource.WalkCommits call in its own
// child span. It always walks from HEAD with no stop commit: the group's
// fields have independent cursors, so the shared walk carries the union of
// what any of them needs and each field slices its own range out of it.
func (p *Poller) walkHistory(ctx context.Context, logger *slog.Logger, t domain.Tracker, filePath string, bound time.Time) (fileHistory, error) {
	var history fileHistory
	err := telemetry.WithSpan(ctx, p.tracer, "gitsource.walk_commits", func(ctx context.Context) error {
		v, err := p.src.WalkCommits(ctx, filePath, "", bound)
		history = v
		return err
	})
	if err != nil {
		logger.Error("poller: walk commits failed", slog.String("repo", t.Repo), slog.String("filePath", filePath), slog.Any("error", err))
		return nil, fmt.Errorf("poller: walk commits for %q: %w", filePath, err)
	}
	return history, nil
}

// pollFieldHistory runs one field's polling cycle against an already-walked
// file history: pick out the field's own range (from its cursor, or from its
// backfill boundary on first run), diff consecutive snapshots, attach facets
// from this file's own path, and persist Changes plus the field's new HWM.
func (p *Poller) pollFieldHistory(ctx context.Context, logger *slog.Logger, m groupMember, filePath, hwm string, history fileHistory) error {
	// We need a "before" snapshot to diff against. When there is no HWM yet
	// (first run), we treat the state before the oldest snapshot as absent.
	var prevField domain.TrackedField
	var snapshots fileHistory

	if hwm == "" {
		// First run: bound this field to its own backfill window, which may be
		// narrower than the shared walk's.
		snapshots = history.notBefore(p.backfillBound(m.tracker))
		if len(snapshots) == 0 {
			return nil
		}
		// Extract state of the very first snapshot as the initial "old" value.
		// Then walk pairs starting from index 1 using the first as old.
		// This means: if there's only one snapshot, we produce an "added" Change.
		var err error
		prevField, err = p.extractField(logger, m, filePath, snapshots[0])
		if err != nil {
			return fmt.Errorf("poller: extract (initial): %w", err)
		}
		if len(snapshots) == 1 {
			// Only one commit ever — treat absent→first commit as "added".
			if err := p.emitChanges(ctx, logger, m, filePath, snapshots[0], domain.TrackedField{Present: false}, prevField); err != nil {
				return err
			}
			return p.setHighWaterMark(ctx, logger, m.tracker, filePath, snapshots[0].CommitSha)
		}
		snapshots = snapshots[1:]
	} else {
		// There IS a previous snapshot already processed. We need the file
		// state at the HWM commit to compute the diff for the first new commit.
		// The shared walk is unbounded whenever any field has a cursor, so an
		// HWM commit predating every backfill window is still present here.
		rest, at, found := history.since(hwm)
		if len(rest) == 0 {
			return nil // nothing new since last poll
		}
		snapshots = rest
		if found {
			var err error
			prevField, err = p.extractField(logger, m, filePath, at)
			if err != nil {
				return fmt.Errorf("poller: extract HWM content: %w", err)
			}
		}
	}

	var lastSha string
	for _, snap := range snapshots {
		newField, err := p.extractField(logger, m, filePath, snap)
		if err != nil {
			return fmt.Errorf("poller: extract at %s: %w", snap.CommitSha, err)
		}
		if err := p.emitChanges(ctx, logger, m, filePath, snap, prevField, newField); err != nil {
			return err
		}
		prevField = newField
		lastSha = snap.CommitSha
	}

	if lastSha != "" {
		return p.setHighWaterMark(ctx, logger, m.tracker, filePath, lastSha)
	}

	return nil
}

// emitChanges diffs old→new for one snapshot and persists every resulting
// Change, attaching facets from the snapshot's own file path.
func (p *Poller) emitChanges(ctx context.Context, logger *slog.Logger, m groupMember, filePath string, snap domain.CommitSnapshot, old, new domain.TrackedField) error {
	params := differ.ScalarParams{
		Repo:        m.tracker.Repo,
		FilePath:    snap.FilePath,
		Field:       m.tracker.Field,
		CommitSha:   snap.CommitSha,
		Author:      snap.Author,
		CommittedAt: snap.CommittedAt,
		Facets:      m.fe.ExtractFacets(snap.FilePath),
		IssueRefs:   issueref.Parse(snap.Message),
		Subject:     commitSubject(snap.Message),
	}
	for _, c := range diffFields(params, old, new) {
		if err := p.saveChange(ctx, logger, m.tracker, filePath, c); err != nil {
			return err
		}
	}
	return nil
}

// extractField wraps a single Extract call, logging and reporting a failure
// to the ExtractFailureRecorder (tagged with the member's engine, e.g. "hcl")
// in one place — every one of pollFieldHistory's three extraction sites
// (initial baseline, HWM-content lookup, and the main per-commit loop) shares
// this so a malformed or unparseable file is consistently logged and counted
// on the poll-health/status surface no matter which site hits it.
//
// m.ex is typed as the extractor.FieldExtractor interface (not the concrete
// gojq-based *extractor.Extractor) so an alternate backend — e.g. the HCL
// extractor — can be substituted without this changing at all.
func (p *Poller) extractField(logger *slog.Logger, m groupMember, filePath string, snap domain.CommitSnapshot) (domain.TrackedField, error) {
	field, err := m.ex.Extract(snap.Content)
	if err != nil {
		logger.Error("poller: extract failed",
			slog.String("repo", m.tracker.Repo),
			slog.String("filePath", filePath),
			slog.String("commitSha", snap.CommitSha),
			slog.String("engine", m.engine),
			slog.Any("error", err))
		p.extractFailures.RecordExtractFailure(m.engine)
		return domain.TrackedField{}, fmt.Errorf("engine=%q: %w", m.engine, err)
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
		return p.st.SetHighWaterMark(t.Repo, filePath, t.Field, sha)
	})
	if err != nil {
		logger.Error("poller: set high-water mark failed", slog.String("repo", t.Repo), slog.String("filePath", filePath), slog.Any("error", err))
		return fmt.Errorf("poller: set HWM: %w", err)
	}
	return nil
}
