package web

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
)

// pollHealthCase is one property-test input: a fixed "now" instant paired
// with a snapshot of tracker statuses spanning adversarial shapes — the
// empty snapshot, "never attempted" zero-value entries mixed with normally
// attempted ones, and any number of failing/succeeding trackers.
type pollHealthCase struct {
	now      time.Time
	snapshot []pollstatus.TrackerStatus
}

// Generate implements quick.Generator. A hand-rolled generator is used
// (rather than testing/quick's default struct reflection) because
// time.Time carries unexported fields quick.Value cannot populate; prior art
// in this package (chart_diff_test.go's unknownRepoCommitPair) follows the
// same pattern.
func (pollHealthCase) Generate(rnd *rand.Rand, size int) reflect.Value {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rnd.Intn(1_000_000)) * time.Minute)

	n := rnd.Intn(12) // 0..11 trackers, including the empty-snapshot case
	snapshot := make([]pollstatus.TrackerStatus, 0, n)
	for i := 0; i < n; i++ {
		var attempt, success, next time.Time

		hasAttempted := rnd.Intn(4) != 0 // ~75% attempted at least once; rest are the zero-value "never" shape
		failed := rnd.Intn(2) == 0
		errMsg := ""
		if hasAttempted {
			attempt = now.Add(-time.Duration(rnd.Intn(10_000)) * time.Minute)
			next = attempt.Add(time.Duration(rnd.Intn(10_000)) * time.Minute)
			if failed {
				errMsg = fmt.Sprintf("synthetic poll failure %d", rnd.Int())
			} else {
				success = attempt
			}
		}

		snapshot = append(snapshot, pollstatus.TrackerStatus{
			Repo:          fmt.Sprintf("repo-%d", i),
			FileGlob:      "Chart.yaml",
			Field:         "version",
			LastAttemptAt: attempt,
			LastSuccessAt: success,
			LastError:     errMsg,
			NextRunAt:     next,
		})
	}

	return reflect.ValueOf(pollHealthCase{now: now, snapshot: snapshot})
}

// TestStatusChip_Invariants_Property asserts the invariants that must hold
// for EVERY snapshot shape, not just the hand-picked examples above: an
// empty snapshot is always "unknown", any failing entry always flips Status
// to "error" with ErrorCount matching exactly, and an all-success
// non-empty snapshot is always "ok" — over generated adversarial inputs
// (empty, zero-value "never attempted" entries, many trackers, mixed
// success/failure). This subsumes the whole class of "what if there are 0,
// 1, or N trackers in some combination of states" cases a table can only
// sample from, and also asserts statusChip never panics on any input shape
// quick.Check draws.
func TestStatusChip_Invariants_Property(t *testing.T) {
	t.Parallel()

	property := func(c pollHealthCase) bool {
		got := statusChip(c.snapshot, nil, c.now)

		wantErrorCount := 0
		for _, ts := range c.snapshot {
			if ts.LastError != "" {
				wantErrorCount++
			}
		}

		switch {
		case len(c.snapshot) == 0:
			return got.Status == statusUnknown && got.ErrorCount == 0 && got.ErrorText == ""
		case wantErrorCount > 0:
			return got.Status == statusError && got.ErrorCount == wantErrorCount && got.ErrorText != ""
		default:
			return got.Status == statusOK && got.ErrorCount == 0 && got.ErrorText == ""
		}
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 200}); err != nil {
		t.Error(err)
	}
}
