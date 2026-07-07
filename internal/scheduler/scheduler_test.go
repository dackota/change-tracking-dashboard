// Package scheduler_test exercises the Scheduler through its public interface
// using a fake clock and a recording poll function.
package scheduler_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/scheduler"
)

// --- test helpers ---

// fakeClock is a manually-advanced clock usable in tests. Callers advance it
// with Advance; the scheduler reads it via Now().
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// recordingPollFn returns a PollFunc that records each (tracker identity) call.
type pollRecorder struct {
	mu    sync.Mutex
	calls []string // tracker identities in call order
}

func (r *pollRecorder) fn(t domain.Tracker) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, trackerID(t))
	return nil
}

func (r *pollRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *pollRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// trackerID returns the canonical identity string for a tracker.
func trackerID(t domain.Tracker) string {
	return t.Repo + "\x00" + t.FileGlob + "\x00" + t.Field
}

// statusCall is one (tracker, at, err) tuple recorded by fakeStatusRecorder.
type statusCall struct {
	tracker domain.Tracker
	at      time.Time
	err     error
}

// fakeStatusRecorder is a test double for scheduler.StatusRecorder. It
// records every call it receives so tests can assert the Scheduler now
// feeds poll outcomes (including errors) into the status seam instead of
// discarding them.
type fakeStatusRecorder struct {
	mu    sync.Mutex
	calls []statusCall
}

func (f *fakeStatusRecorder) Record(t domain.Tracker, at time.Time, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, statusCall{tracker: t, at: at, err: err})
}

func (f *fakeStatusRecorder) snapshot() []statusCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]statusCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// makeTracker creates a minimal tracker with the given poll interval.
func makeTracker(repo, glob, field string, pollSecs int) domain.Tracker {
	return domain.Tracker{
		Repo:                repo,
		FileGlob:            glob,
		Field:               field,
		ExtractorExpr:       ".version",
		FacetPattern:        "",
		PollIntervalSeconds: pollSecs,
		BackfillDays:        90,
	}
}

// --- Behavior 1: tracker is polled immediately when first seen ---

func TestScheduler_NewTracker_PolledPromptly(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &pollRecorder{}
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)

	sched := scheduler.New(clk.Now, scheduler.PollFunc(rec.fn), &fakeStatusRecorder{})

	// Tick with the tracker present — it has never been polled, so it fires.
	sched.Tick([]domain.Tracker{tr})

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 poll call after first Tick, got %d", len(calls))
	}
	if calls[0] != trackerID(tr) {
		t.Errorf("call[0] = %q, want %q", calls[0], trackerID(tr))
	}
}

// --- Behavior 2: tracker is NOT re-polled before its interval elapses ---

func TestScheduler_TrackerNotRepolledBeforeInterval(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &pollRecorder{}
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)

	sched := scheduler.New(clk.Now, scheduler.PollFunc(rec.fn), &fakeStatusRecorder{})

	// First tick: polls immediately.
	sched.Tick([]domain.Tracker{tr})
	if rec.count() != 1 {
		t.Fatalf("expected 1 call after first tick, got %d", rec.count())
	}

	// Advance 30s (half the interval) and tick again — should NOT poll.
	clk.Advance(30 * time.Second)
	sched.Tick([]domain.Tracker{tr})
	if rec.count() != 1 {
		t.Fatalf("expected still 1 call at 30s (interval=60s), got %d", rec.count())
	}
}

// --- Behavior 3: tracker IS re-polled once interval elapses ---

func TestScheduler_TrackerRepolledAfterInterval(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &pollRecorder{}
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)

	sched := scheduler.New(clk.Now, scheduler.PollFunc(rec.fn), &fakeStatusRecorder{})

	sched.Tick([]domain.Tracker{tr}) // poll 1 at t=0
	clk.Advance(60 * time.Second)
	sched.Tick([]domain.Tracker{tr}) // poll 2 at t=60s (exactly at interval)

	if rec.count() != 2 {
		t.Fatalf("expected 2 poll calls after interval elapsed, got %d", rec.count())
	}
}

// --- Behavior 4: two trackers with different intervals each fire on their own cadence ---

func TestScheduler_TwoTrackers_DifferentIntervals(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &pollRecorder{}

	// trA: 60s interval; trB: 120s interval
	trA := makeTracker("/repo/a", "Chart.yaml", "version", 60)
	trB := makeTracker("/repo/b", "Chart.yaml", "version", 120)

	sched := scheduler.New(clk.Now, scheduler.PollFunc(rec.fn), &fakeStatusRecorder{})

	// t=0: both polled for the first time
	sched.Tick([]domain.Tracker{trA, trB})
	if rec.count() != 2 {
		t.Fatalf("expected 2 initial polls, got %d", rec.count())
	}

	// t=60s: trA's interval elapsed → trA polled; trB not yet
	clk.Advance(60 * time.Second)
	sched.Tick([]domain.Tracker{trA, trB})
	if rec.count() != 3 {
		t.Fatalf("at 60s: expected 3 total polls (trA fired again), got %d", rec.count())
	}

	// Verify the third call was for trA.
	calls := rec.snapshot()
	if calls[2] != trackerID(trA) {
		t.Errorf("calls[2] = %q, want trA=%q", calls[2], trackerID(trA))
	}

	// t=120s: both intervals elapsed → both polled
	clk.Advance(60 * time.Second)
	sched.Tick([]domain.Tracker{trA, trB})
	if rec.count() != 5 {
		t.Fatalf("at 120s: expected 5 total polls (both fired), got %d", rec.count())
	}
}

// --- Behavior 5: a newly-appearing tracker is polled on the Tick it first appears ---

func TestScheduler_NewlyAppearedTracker_PolledPromptly(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &pollRecorder{}

	trA := makeTracker("/repo/a", "Chart.yaml", "version", 60)
	trB := makeTracker("/repo/b", "Chart.yaml", "version", 60)

	sched := scheduler.New(clk.Now, scheduler.PollFunc(rec.fn), &fakeStatusRecorder{})

	// t=0: only trA
	sched.Tick([]domain.Tracker{trA})
	if rec.count() != 1 {
		t.Fatalf("expected 1 poll for trA, got %d", rec.count())
	}

	// t=30s: trB appears (config reload added it) — trA not due yet; trB new → fires
	clk.Advance(30 * time.Second)
	sched.Tick([]domain.Tracker{trA, trB})
	if rec.count() != 2 {
		t.Fatalf("expected 2 total polls (trB new at 30s), got %d", rec.count())
	}
	calls := rec.snapshot()
	if calls[1] != trackerID(trB) {
		t.Errorf("calls[1] = %q, want trB=%q", calls[1], trackerID(trB))
	}
}

// --- Behavior 6: a removed tracker stops being polled ---

func TestScheduler_RemovedTracker_StopsBeingPolled(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &pollRecorder{}

	trA := makeTracker("/repo/a", "Chart.yaml", "version", 60)
	trB := makeTracker("/repo/b", "Chart.yaml", "version", 60)

	sched := scheduler.New(clk.Now, scheduler.PollFunc(rec.fn), &fakeStatusRecorder{})

	// t=0: both trackers
	sched.Tick([]domain.Tracker{trA, trB})
	if rec.count() != 2 {
		t.Fatalf("expected 2 polls at t=0, got %d", rec.count())
	}

	// t=60s: trB removed from config (reload); only trA present
	clk.Advance(60 * time.Second)
	sched.Tick([]domain.Tracker{trA}) // trB not included
	// Only trA should fire; trB gone.
	if rec.count() != 3 {
		t.Fatalf("at 60s with trB removed: expected 3 total polls, got %d", rec.count())
	}
	calls := rec.snapshot()
	if calls[2] != trackerID(trA) {
		t.Errorf("calls[2] = %q, want trA=%q", calls[2], trackerID(trA))
	}

	// t=120s: still only trA — trB must NOT re-appear
	clk.Advance(60 * time.Second)
	sched.Tick([]domain.Tracker{trA})
	if rec.count() != 4 {
		t.Fatalf("at 120s: expected 4 total polls, got %d", rec.count())
	}
}

// --- Behavior 7: a successful poll is fed into the status recorder ---

func TestScheduler_PollSuccess_FeedsIntoStatusRecorder(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &pollRecorder{}
	status := &fakeStatusRecorder{}
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)

	sched := scheduler.New(clk.Now, scheduler.PollFunc(rec.fn), status)
	sched.Tick([]domain.Tracker{tr})

	calls := status.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 status Record call, got %d", len(calls))
	}
	if calls[0].err != nil {
		t.Errorf("Record err = %v, want nil for a successful poll", calls[0].err)
	}
	if !calls[0].at.Equal(clk.Now()) {
		t.Errorf("Record at = %v, want %v", calls[0].at, clk.Now())
	}
	if trackerID(calls[0].tracker) != trackerID(tr) {
		t.Errorf("Record tracker = %+v, want %+v", calls[0].tracker, tr)
	}
}

// --- Behavior 8: a poll error is fed into the status recorder — it is no
// longer silently discarded, which is the defect this slice exists to fix. ---

func TestScheduler_PollError_FeedsIntoStatusRecorder(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	status := &fakeStatusRecorder{}
	tr := makeTracker("/repo/a", "Chart.yaml", "version", 60)

	wantErr := errors.New("clone failed: connection refused")
	failingPoll := func(domain.Tracker) error { return wantErr }

	sched := scheduler.New(clk.Now, scheduler.PollFunc(failingPoll), status)
	sched.Tick([]domain.Tracker{tr})

	calls := status.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 status Record call, got %d", len(calls))
	}
	if !errors.Is(calls[0].err, wantErr) {
		t.Errorf("Record err = %v, want %v (the poll error must reach the status recorder, not be dropped)", calls[0].err, wantErr)
	}
	if !calls[0].at.Equal(clk.Now()) {
		t.Errorf("Record at = %v, want %v", calls[0].at, clk.Now())
	}
}
