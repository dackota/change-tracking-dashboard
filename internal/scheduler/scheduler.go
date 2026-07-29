// Package scheduler provides a hot-reload-aware scheduler that polls each
// domain.Tracker at its own configured cadence (PollIntervalSeconds).
//
// Design: a single-type Scheduler drives all trackers from one Tick() call.
// The caller (cmd/dashboard/main.go) runs a background goroutine that calls
// Tick(cfgWatcher.Current().Trackers) on a fixed base interval (e.g. 1s),
// passing the latest tracker list each time. The Scheduler tracks the last
// time each tracker was polled and fires those whose interval has elapsed.
//
// This avoids one-goroutine-per-tracker, making add/remove on hot-reload
// trivially safe — the scheduler simply consults the current list on each Tick.
package scheduler

import (
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// BaseTickInterval is the resolution at which the scheduler's background loop
// should call Tick. Finer intervals give better cadence accuracy but use more
// CPU. One second is a good balance for poll intervals measured in minutes.
// The caller (cmd/dashboard/main.go) uses this constant for the ticker period.
const BaseTickInterval = 1 * time.Second

// GroupPollFunc is the callback invoked by the Scheduler for each group of
// due trackers that share one (Repo, FileGlob). It matches
// poller.Poller.PollGroup's signature so the real poller can be plugged in
// directly, and returns one error per input tracker, index-aligned.
//
// The Scheduler groups rather than polling tracker-by-tracker because every
// field watched in the same file set derives from the same commit histories:
// polling them together lets the poller walk each matched file once instead of
// once per field (#137).
type GroupPollFunc func([]domain.Tracker) []error

// PollFunc is a per-tracker poll callback, matching poller.Poller.Poll's
// signature. Use PerTracker to adapt one into the GroupPollFunc New takes.
type PollFunc func(domain.Tracker) error

// PerTracker adapts a per-tracker PollFunc into a GroupPollFunc that polls a
// group's trackers one at a time. It gives up the shared-walk saving grouping
// exists for, so it is for callers that genuinely have no group form — not the
// production wiring.
func PerTracker(fn PollFunc) GroupPollFunc {
	return func(ts []domain.Tracker) []error {
		errs := make([]error, len(ts))
		for i, t := range ts {
			errs[i] = fn(t)
		}
		return errs
	}
}

// StatusRecorder is the seam through which the Scheduler reports the outcome
// of every poll attempt (success or failure). pollstatus.Registry satisfies
// this interface directly; tests may substitute a fake recorder without
// importing that package. Record is called exactly once per Tick per due
// tracker, with the same clock reading (now) the due-calculation used and the
// exact error PollFunc returned (nil on success) — so a poll error is
// reported here, not just logged and discarded.
type StatusRecorder interface {
	Record(t domain.Tracker, at time.Time, err error)
}

// trackerState holds the last-polled time for a single tracker identity.
type trackerState struct {
	lastPolledAt time.Time
}

// trackerKey is the canonical identity of a tracker for scheduling purposes.
// It must be unique per flattened tracker.
func trackerKey(t domain.Tracker) string {
	return t.Repo + "\x00" + t.FileGlob + "\x00" + t.Field
}

// groupKey is the identity of the tracker GROUP t belongs to: every tracker
// watching the same file set in the same repo, regardless of field. It stops
// at (Repo, FileGlob) deliberately — Repo decides which git source the poller
// holds, and FileGlob decides the file set walked, so those are exactly the
// two things a group's members must agree on.
func groupKey(t domain.Tracker) string {
	return t.Repo + "\x00" + t.FileGlob
}

// Scheduler tracks per-tracker last-polled times and fires the poll function
// whenever a tracker's interval has elapsed. It is NOT safe for concurrent
// Tick calls; the caller must serialize them (which is natural when driven from
// a single background goroutine).
type Scheduler struct {
	now    func() time.Time
	poll   GroupPollFunc
	status StatusRecorder
	state  map[string]trackerState
}

// New returns a Scheduler that uses the provided clock and group poll
// function, reporting every poll outcome to status. now is injectable so tests
// can use a fake clock for deterministic behavior.
func New(now func() time.Time, poll GroupPollFunc, status StatusRecorder) *Scheduler {
	return &Scheduler{
		now:    now,
		poll:   poll,
		status: status,
		state:  make(map[string]trackerState),
	}
}

// Tick evaluates the current tracker list and calls the poll function for the
// trackers whose interval has elapsed since their last poll (or which have
// never been polled), batched into groups that share one (Repo, FileGlob).
// Removed trackers (absent from trackers) are implicitly evicted from state on
// the next GC pass below.
//
// Only DUE trackers are grouped: a slow-cadence field sharing a glob with a
// fast one is not dragged along early, so per-field cadence is unchanged.
//
// Tick is designed to be called on a fixed base interval from a single
// goroutine; it is NOT goroutine-safe.
func (s *Scheduler) Tick(trackers []domain.Tracker) {
	now := s.now()

	// Build a set of active tracker keys so we can garbage-collect stale state.
	activeKeys := make(map[string]struct{}, len(trackers))
	// Groups of due trackers, in first-seen order so poll order stays a
	// deterministic function of the config's tracker order.
	var groups [][]domain.Tracker
	groupIndex := make(map[string]int)

	for _, t := range trackers {
		activeKeys[trackerKey(t)] = struct{}{}

		if !s.isDue(t, now) {
			continue
		}

		gk := groupKey(t)
		if i, ok := groupIndex[gk]; ok {
			groups[i] = append(groups[i], t)
			continue
		}
		groupIndex[gk] = len(groups)
		groups = append(groups, []domain.Tracker{t})
	}

	for _, group := range groups {
		s.pollGroup(group, now)
	}

	// Evict state for trackers that are no longer in the active list.
	// This prevents unbounded state growth when trackers are removed via config reload.
	for key := range s.state {
		if _, ok := activeKeys[key]; !ok {
			delete(s.state, key)
		}
	}
}

// isDue reports whether t's poll interval has elapsed as of now. A tracker
// with zero interval is treated as "fire on every Tick".
func (s *Scheduler) isDue(t domain.Tracker, now time.Time) bool {
	st := s.state[trackerKey(t)]
	interval := time.Duration(t.PollIntervalSeconds) * time.Second
	return st.lastPolledAt.IsZero() || interval == 0 || now.Sub(st.lastPolledAt) >= interval
}

// pollGroup polls one group of due trackers and records each member's own
// outcome.
//
// Report every outcome — success or failure — to the status recorder. Errors
// used to be logged and then dropped; they are now fed into pollstatus so
// LastError/LastSuccessAt reflect reality.
//
// GroupPollFunc's signature carries no context: the poll cycle's own
// trace/span is created inside its implementation (poller.PollGroup), not
// visible here. A scheduler-side error log would therefore have no
// trace_id/span_id to correlate with — and would duplicate the identical error
// poller.PollGroup already logs at ERROR level, correlated to its own
// poll-cycle span (criterion 4). So the error is reported to the status
// recorder and left for the poller to log; it is deliberately not logged a
// second time here.
func (s *Scheduler) pollGroup(group []domain.Tracker, now time.Time) {
	errs := s.poll(group)

	for i, t := range group {
		// A PollFunc that returns a short slice is a contract violation, not a
		// reason to panic mid-cycle: treat the missing entries as "no error
		// reported" and keep the remaining trackers' bookkeeping intact.
		var err error
		if i < len(errs) {
			err = errs[i]
		}
		s.status.Record(t, now, err)
		s.state[trackerKey(t)] = trackerState{lastPolledAt: now}
	}
}
