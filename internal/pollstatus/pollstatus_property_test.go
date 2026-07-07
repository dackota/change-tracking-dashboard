package pollstatus_test

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/pollstatus"
)

// TestRegistry_ConcurrentRecordAndSnapshot_Invariants_Property asserts the
// operational invariants that must hold under adversarial concurrent access
// — many goroutines racing Record calls (mixed success/failure) against the
// same and different tracker identities, interleaved with concurrent
// Snapshot reads — not just the single-goroutine example tests above:
//
//   - no data race (run with -race) and no panic
//   - every tracker recorded during the run is present exactly once in the
//     final Snapshot
//   - NextRunAt is always self-consistent: NextRunAt == LastAttemptAt +
//     that tracker's interval, for every entry, every time
//   - LastAttemptAt is never zero for a tracker that was recorded at least
//     once (no lost/torn write)
//   - Snapshot is deterministic: two calls after the writers have finished
//     return identical, identically-ordered results (no torn read leaking a
//     half-updated entry, no nondeterministic map-iteration order escaping)
func TestRegistry_ConcurrentRecordAndSnapshot_Invariants_Property(t *testing.T) {
	t.Parallel()

	reg := pollstatus.New()

	trackers := []domain.Tracker{
		makeTracker("/repo/a", "Chart.yaml", "version", 30),
		makeTracker("/repo/b", "values.yaml", "image.tag", 45),
		makeTracker("/repo/c", "Chart.yaml", "appVersion", 60),
	}

	const goroutinesPerTracker = 20
	const recordsPerGoroutine = 50
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for _, tr := range trackers {
		for g := 0; g < goroutinesPerTracker; g++ {
			wg.Add(1)
			go func(tr domain.Tracker, seed int) {
				defer wg.Done()
				for i := 0; i < recordsPerGoroutine; i++ {
					at := base.Add(time.Duration(seed*recordsPerGoroutine+i) * time.Second)
					var err error
					if i%3 == 0 {
						err = fmt.Errorf("synthetic failure goroutine=%d iter=%d", seed, i)
					}
					reg.Record(tr, at, err)
					_ = reg.Snapshot() // exercise concurrent reads interleaved with writes
				}
			}(tr, g)
		}
	}
	wg.Wait()

	got := reg.Snapshot()
	if len(got) != len(trackers) {
		t.Fatalf("Snapshot() len = %d, want %d (one entry per distinct tracker recorded)", len(got), len(trackers))
	}

	intervalOf := func(ts pollstatus.TrackerStatus) time.Duration {
		for _, tr := range trackers {
			if tr.Repo == ts.Repo && tr.FileGlob == ts.FileGlob && tr.Field == ts.Field {
				return time.Duration(tr.PollIntervalSeconds) * time.Second
			}
		}
		t.Fatalf("no matching tracker for status %+v", ts)
		return 0
	}

	for _, ts := range got {
		if ts.LastAttemptAt.IsZero() {
			t.Errorf("tracker %s/%s/%s: LastAttemptAt is zero after %d concurrent Records", ts.Repo, ts.FileGlob, ts.Field, goroutinesPerTracker*recordsPerGoroutine)
		}
		want := ts.LastAttemptAt.Add(intervalOf(ts))
		if !ts.NextRunAt.Equal(want) {
			t.Errorf("tracker %s/%s/%s: NextRunAt = %v, want LastAttemptAt+interval = %v", ts.Repo, ts.FileGlob, ts.Field, ts.NextRunAt, want)
		}
	}

	// Determinism: once writers are done, repeated Snapshot calls must agree
	// exactly, including order.
	again := reg.Snapshot()
	if !reflect.DeepEqual(got, again) {
		t.Errorf("Snapshot() not deterministic across consecutive calls:\nfirst:  %+v\nsecond: %+v", got, again)
	}
}
