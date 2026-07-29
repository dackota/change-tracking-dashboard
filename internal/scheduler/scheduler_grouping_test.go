package scheduler_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/scheduler"
)

// groupRecorder records each GroupPollFunc invocation as one batch of tracker
// identities, so tests can assert not just WHICH trackers were polled but how
// they were batched — the whole point of grouping (#137).
type groupRecorder struct {
	mu      sync.Mutex
	batches [][]string
	errFor  map[string]error // tracker identity -> error to return
}

func (r *groupRecorder) fn(ts []domain.Tracker) []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	batch := make([]string, 0, len(ts))
	errs := make([]error, len(ts))
	for i, t := range ts {
		batch = append(batch, trackerID(t))
		errs[i] = r.errFor[trackerID(t)]
	}
	r.batches = append(r.batches, batch)
	return errs
}

// summary renders the recorded batches as "field+field|field" — batch members
// joined by "+", batches separated by "|" — for readable assertions.
func (r *groupRecorder) summary() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, len(r.batches))
	for _, batch := range r.batches {
		fields := make([]string, 0, len(batch))
		for _, id := range batch {
			parts := strings.Split(id, "\x00")
			fields = append(fields, parts[len(parts)-1])
		}
		out = append(out, strings.Join(fields, "+"))
	}
	return strings.Join(out, "|")
}

// TestScheduler_GroupsDueTrackersSharingRepoAndGlob is the scheduler half of
// issue #137: trackers that share one (Repo, FileGlob) and are due on the same
// Tick must reach the poller as ONE group, so the poller can walk each matched
// file's history once instead of once per field.
func TestScheduler_GroupsDueTrackersSharingRepoAndGlob(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &groupRecorder{}
	sched := scheduler.New(clk.Now, rec.fn, &fakeStatusRecorder{})

	sched.Tick([]domain.Tracker{
		makeTracker("/repo/a", "terraform/*.tf", "version", 60),
		makeTracker("/repo/a", "terraform/*.tf", "region", 60),
		makeTracker("/repo/a", "terraform/*.tf", "node-count", 60),
	})

	if got := rec.summary(); got != "version+region+node-count" {
		t.Errorf("batches = %q, want one batch %q", got, "version+region+node-count")
	}
}

// TestScheduler_SeparatesGroupsByRepoAndGlob verifies the grouping key is
// (Repo, FileGlob): a group must never span two repos (the poller holds one
// repo's git source) or two globs (different file sets).
func TestScheduler_SeparatesGroupsByRepoAndGlob(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &groupRecorder{}
	sched := scheduler.New(clk.Now, rec.fn, &fakeStatusRecorder{})

	sched.Tick([]domain.Tracker{
		makeTracker("/repo/a", "terraform/*.tf", "tf-version", 60),
		makeTracker("/repo/a", "*/Chart.yaml", "chart-version", 60),
		makeTracker("/repo/a", "terraform/*.tf", "tf-region", 60),
		makeTracker("/repo/b", "terraform/*.tf", "other-version", 60),
	})

	// Groups appear in first-seen order; members in list order within a group.
	const want = "tf-version+tf-region|chart-version|other-version"
	if got := rec.summary(); got != want {
		t.Errorf("batches = %q, want %q", got, want)
	}
}

// TestScheduler_GroupsOnlyDueTrackers verifies grouping does not drag a
// not-yet-due tracker into a due group — each field keeps its own cadence.
func TestScheduler_GroupsOnlyDueTrackers(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	rec := &groupRecorder{}
	sched := scheduler.New(clk.Now, rec.fn, &fakeStatusRecorder{})

	fast := makeTracker("/repo/a", "terraform/*.tf", "fast", 60)
	slow := makeTracker("/repo/a", "terraform/*.tf", "slow", 600)

	sched.Tick([]domain.Tracker{fast, slow}) // both due (never polled)
	clk.Advance(90 * time.Second)
	sched.Tick([]domain.Tracker{fast, slow}) // only fast is due

	const want = "fast+slow|fast"
	if got := rec.summary(); got != want {
		t.Errorf("batches = %q, want %q", got, want)
	}
}

// TestScheduler_RecordsPerTrackerOutcomeWithinGroup verifies each grouped
// tracker's own outcome reaches the status recorder: one member's failure must
// not be attributed to its siblings.
func TestScheduler_RecordsPerTrackerOutcomeWithinGroup(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	boom := errors.New("boom")
	bad := makeTracker("/repo/a", "terraform/*.tf", "bad", 60)
	good := makeTracker("/repo/a", "terraform/*.tf", "good", 60)

	rec := &groupRecorder{errFor: map[string]error{trackerID(bad): boom}}
	status := &fakeStatusRecorder{}
	sched := scheduler.New(clk.Now, rec.fn, status)

	sched.Tick([]domain.Tracker{good, bad})

	calls := status.snapshot()
	if len(calls) != 2 {
		t.Fatalf("status calls = %d, want 2 (one per grouped tracker)", len(calls))
	}
	byField := map[string]error{}
	for _, c := range calls {
		byField[c.tracker.Field] = c.err
		if !c.at.Equal(clk.Now()) {
			t.Errorf("status at = %v, want the Tick's clock reading %v", c.at, clk.Now())
		}
	}
	if byField["good"] != nil {
		t.Errorf("status err for good = %v, want nil", byField["good"])
	}
	if !errors.Is(byField["bad"], boom) {
		t.Errorf("status err for bad = %v, want %v", byField["bad"], boom)
	}
}

// TestScheduler_ShortGroupErrorSlice_DoesNotPanic guards the seam against a
// misbehaving PollFunc: a group poll that returns fewer errors than trackers
// must degrade to "outcome unknown", never index out of range.
func TestScheduler_ShortGroupErrorSlice_DoesNotPanic(t *testing.T) {
	t.Parallel()

	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	status := &fakeStatusRecorder{}
	short := func([]domain.Tracker) []error { return nil }
	sched := scheduler.New(clk.Now, short, status)

	sched.Tick([]domain.Tracker{
		makeTracker("/repo/a", "terraform/*.tf", "a", 60),
		makeTracker("/repo/a", "terraform/*.tf", "b", 60),
	})

	if got := len(status.snapshot()); got != 2 {
		t.Errorf("status calls = %d, want 2", got)
	}
}
