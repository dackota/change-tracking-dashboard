// Package pollstatus records, per tracker, the outcome of each poll attempt
// — when it was last attempted, when it last succeeded, and what error (if
// any) occurred — and derives when each tracker is next due to run.
//
// It is a deep, concurrency-safe, in-process module with no persistence: a
// Registry's state is rebuilt naturally from the next round of Record calls
// after a process restart, and its only public surface is Record (report one
// poll outcome) and Snapshot (read every tracker's current status). The web
// layer (a separate slice) consumes Snapshot to expose poll health without
// needing to know anything about how the Registry tracks state internally.
package pollstatus

import (
	"sort"
	"sync"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// TrackerStatus is a point-in-time, immutable snapshot of one tracker's poll
// history. The zero value represents a tracker that has never been polled:
// LastAttemptAt and LastSuccessAt are the zero time.Time, LastError is empty,
// and NextRunAt is the zero time.Time.
type TrackerStatus struct {
	Repo, FileGlob, Field string

	// LastAttemptAt is when this tracker's poll function was last invoked,
	// regardless of outcome. Zero means never attempted.
	LastAttemptAt time.Time
	// LastSuccessAt is when this tracker's poll function last returned a nil
	// error. Zero means never succeeded.
	LastSuccessAt time.Time
	// LastError is the error message from the most recent poll attempt, or
	// empty when that attempt succeeded (or none has occurred yet).
	LastError string
	// NextRunAt is when this tracker is next due to be polled, derived as
	// LastAttemptAt plus its configured poll interval.
	NextRunAt time.Time
}

// entry is the mutable record the Registry keeps per tracker identity. It
// deliberately holds only the fields poll status needs — not the full
// domain.Tracker (whose ExtractorExpr, FacetPattern, and BackfillDays are
// irrelevant here) — and is never exposed directly: Snapshot copies its
// fields into a TrackerStatus.
type entry struct {
	trackerKey
	pollIntervalSeconds int
	lastAttemptAt       time.Time
	lastSuccessAt       time.Time
	lastError           string
}

// Registry records per-tracker poll outcomes and derives each tracker's
// next-due time. It is safe for concurrent use: Record and Snapshot may be
// called from any number of goroutines at once.
//
// The zero Registry is not ready to use — construct one with New.
type Registry struct {
	mu               sync.Mutex
	entries          map[trackerKey]*entry
	extractFailures  map[string]int64 // keyed by engine (e.g. "hcl", "jq")
	planDiffOutcomes map[string]int64 // keyed by plandiff.Kind string (e.g. "ok", "exceeded-limits")
}

// New returns an empty Registry, ready to record poll outcomes.
func New() *Registry {
	return &Registry{
		entries:          make(map[trackerKey]*entry),
		extractFailures:  make(map[string]int64),
		planDiffOutcomes: make(map[string]int64),
	}
}

// trackerKey is the canonical identity of a tracker for lookup purposes —
// the same (repo, file-glob, field) triple the scheduler keys its own due
// calculation by.
type trackerKey struct {
	repo, fileGlob, field string
}

func keyOf(t domain.Tracker) trackerKey {
	return trackerKey{repo: t.Repo, fileGlob: t.FileGlob, field: t.Field}
}

// Record reports the outcome of one poll attempt for tracker t at time at.
// err is the error the poll returned (nil on success).
//
// LastAttemptAt always advances to at, regardless of outcome. A nil err
// clears LastError and advances LastSuccessAt to at; a non-nil err sets
// LastError to err.Error() and leaves LastSuccessAt unchanged from whatever
// it was after the last success.
//
// t's PollIntervalSeconds is captured on every call so Snapshot can derive
// NextRunAt even if the tracker's configured interval changes between polls
// (e.g. via config hot-reload) — the interval used is always the one from the
// most recent Record call.
func (r *Registry) Record(t domain.Tracker, at time.Time, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := keyOf(t)
	e, ok := r.entries[k]
	if !ok {
		e = &entry{trackerKey: k}
		r.entries[k] = e
	}

	e.pollIntervalSeconds = t.PollIntervalSeconds
	e.lastAttemptAt = at
	if err != nil {
		e.lastError = err.Error()
		return
	}
	e.lastError = ""
	e.lastSuccessAt = at
}

// Snapshot returns a deterministically-ordered (by Repo, then FileGlob, then
// Field) copy of every tracker's current status. The returned slice and its
// elements share no mutable state with the Registry: mutating the result
// never affects a subsequent Snapshot call, and vice versa.
func (r *Registry) Snapshot() []TrackerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]TrackerStatus, 0, len(r.entries))
	for _, e := range r.entries {
		interval := time.Duration(e.pollIntervalSeconds) * time.Second
		out = append(out, TrackerStatus{
			Repo:          e.repo,
			FileGlob:      e.fileGlob,
			Field:         e.field,
			LastAttemptAt: e.lastAttemptAt,
			LastSuccessAt: e.lastSuccessAt,
			LastError:     e.lastError,
			NextRunAt:     e.lastAttemptAt.Add(interval),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if a.FileGlob != b.FileGlob {
			return a.FileGlob < b.FileGlob
		}
		return a.Field < b.Field
	})

	return out
}

// RecordExtractFailure increments, by one, the running count of
// FieldExtractor.Extract failures for the given engine (e.g. "hcl", "jq") —
// a file that failed to parse/extract and was skipped, per-engine so an HCL
// tracker's structural parse failures are never conflated with a jq
// tracker's evaluation failures. Counts are process-lifetime totals: like
// the rest of the Registry, they hold no persistence and reset on restart.
func (r *Registry) RecordExtractFailure(engine string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extractFailures[engine]++
}

// ExtractFailureCounts returns an independent copy of the current per-engine
// extract-failure counts (e.g. {"hcl": 3}) — the poll-health/status surface
// this is reported through. An engine that has never failed is simply absent
// from the map, never present with a zero value.
func (r *Registry) ExtractFailureCounts() map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]int64, len(r.extractFailures))
	for engine, n := range r.extractFailures {
		out[engine] = n
	}
	return out
}

// RecordPlanDiffOutcome increments, by one, the running count of
// plandiff.Engine.Diff outcomes for the given Kind (e.g. "ok",
// "exceeded-limits", "could-not-render", "no-prior-version") — acceptance
// criterion 9's "plandiff outcome counts are reported on the poll-health/
// status surface". kind is a plain string (rather than plandiff.Kind) so
// this package never needs to import plandiff, mirroring how
// RecordExtractFailure takes a plain engine string rather than importing
// extractor.
func (r *Registry) RecordPlanDiffOutcome(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.planDiffOutcomes[kind]++
}

// PlanDiffOutcomeCounts returns an independent copy of the current per-Kind
// plandiff outcome counts (e.g. {"ok": 12, "exceeded-limits": 1}) — the
// poll-health/status surface this is reported through. A Kind that has never
// occurred is simply absent from the map, never present with a zero value.
func (r *Registry) PlanDiffOutcomeCounts() map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]int64, len(r.planDiffOutcomes))
	for kind, n := range r.planDiffOutcomes {
		out[kind] = n
	}
	return out
}
