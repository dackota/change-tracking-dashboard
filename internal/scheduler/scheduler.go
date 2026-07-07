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

// PollFunc is the callback invoked by the Scheduler for each tracker that is
// due for a poll. It matches poller.Poller.Poll's signature so the real poller
// can be plugged in directly.
type PollFunc func(domain.Tracker) error

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

// Scheduler tracks per-tracker last-polled times and fires the poll function
// whenever a tracker's interval has elapsed. It is NOT safe for concurrent
// Tick calls; the caller must serialize them (which is natural when driven from
// a single background goroutine).
type Scheduler struct {
	now    func() time.Time
	poll   PollFunc
	status StatusRecorder
	state  map[string]trackerState
}

// New returns a Scheduler that uses the provided clock and poll function,
// reporting every poll outcome to status. now is injectable so tests can use
// a fake clock for deterministic behavior.
func New(now func() time.Time, poll PollFunc, status StatusRecorder) *Scheduler {
	return &Scheduler{
		now:    now,
		poll:   poll,
		status: status,
		state:  make(map[string]trackerState),
	}
}

// Tick evaluates the current tracker list and calls the poll function for each
// tracker whose interval has elapsed since its last poll (or which has never
// been polled). Removed trackers (absent from trackers) are implicitly evicted
// from state on the next GC pass below.
//
// Tick is designed to be called on a fixed base interval from a single
// goroutine; it is NOT goroutine-safe.
func (s *Scheduler) Tick(trackers []domain.Tracker) {
	now := s.now()

	// Build a set of active tracker keys so we can garbage-collect stale state.
	activeKeys := make(map[string]struct{}, len(trackers))
	for _, t := range trackers {
		key := trackerKey(t)
		activeKeys[key] = struct{}{}

		st := s.state[key]
		interval := time.Duration(t.PollIntervalSeconds) * time.Second

		// A tracker with zero interval is treated as "fire on every Tick".
		isDue := st.lastPolledAt.IsZero() || interval == 0 || now.Sub(st.lastPolledAt) >= interval
		if !isDue {
			continue
		}

		// Report every outcome — success or failure — to the status
		// recorder. Errors used to be logged and then dropped; they are now
		// fed into pollstatus so LastError/LastSuccessAt reflect reality.
		//
		// PollFunc's signature (func(domain.Tracker) error) carries no
		// context: the poll cycle's own trace/span is created inside
		// PollFunc's implementation (poller.Poll), not visible here. A
		// scheduler-side error log would therefore have no trace_id/span_id
		// to correlate with — and would duplicate the identical error
		// poller.Poll already logs at ERROR level, correlated to its own
		// poll-cycle span (criterion 4). So the error is reported to the
		// status recorder (below) and left for the poller to log; it is
		// deliberately not logged a second time here.
		err := s.poll(t)
		s.status.Record(t, now, err)

		s.state[key] = trackerState{lastPolledAt: now}
	}

	// Evict state for trackers that are no longer in the active list.
	// This prevents unbounded state growth when trackers are removed via config reload.
	for key := range s.state {
		if _, ok := activeKeys[key]; !ok {
			delete(s.state, key)
		}
	}
}
