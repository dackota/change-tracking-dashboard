package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/pollstatus"
)

// TestStatusChip_NoTrackersEverPolled_ReturnsUnknownStatus verifies R11's
// "quiet vs. broken" baseline: an empty snapshot (the shape Snapshot()
// returns before the scheduler has recorded a single poll attempt) must
// render an explicit "never polled" state, not a nonsensical zero-time
// phrase.
func TestStatusChip_NoTrackersEverPolled_ReturnsUnknownStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	got := statusChip(nil, now)

	want := statusChipView{
		Status:       statusUnknown,
		LastPollText: "Last poll: never",
		NextPollText: "Next poll: —",
	}
	if got != want {
		t.Errorf("statusChip(nil, now) = %+v, want %+v", got, want)
	}
}

// TestStatusChip_AllSuccessSnapshot_ReturnsOKStatusWithRelativeText verifies
// R11's "quiet" (healthy) case: every tracker's last poll attempt succeeded
// — the chip reports "ok" with a relative last-poll phrase from the most
// recent attempt and a relative next-poll phrase from the soonest next-due
// time, and carries no error text.
func TestStatusChip_AllSuccessSnapshot_ReturnsOKStatusWithRelativeText(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	snapshot := []pollstatus.TrackerStatus{
		{
			Repo: "repo-a", FileGlob: "Chart.yaml", Field: "version",
			LastAttemptAt: now.Add(-2 * time.Minute),
			LastSuccessAt: now.Add(-2 * time.Minute),
			NextRunAt:     now.Add(58 * time.Minute),
		},
	}

	got := statusChip(snapshot, now)

	want := statusChipView{
		Status:       statusOK,
		LastPollText: "Last poll: 2 minutes ago",
		NextPollText: "Next poll: in 58 minutes",
	}
	if got != want {
		t.Errorf("statusChip(snapshot, now) = %+v, want %+v", got, want)
	}
}

// TestStatusChip_AnyTrackerErrored_ReturnsErrorStatusWithCount verifies
// R11/R7 (the story): the chip flips to "error" the moment any one tracker's
// last poll attempt failed, even when every other tracker is healthy — and
// it never echoes the raw, potentially internal-detail-bearing LastError
// text (that stays scoped to the Trackers view's per-tracker column, R12).
func TestStatusChip_AnyTrackerErrored_ReturnsErrorStatusWithCount(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	snapshot := []pollstatus.TrackerStatus{
		{
			Repo: "repo-a", FileGlob: "Chart.yaml", Field: "version",
			LastAttemptAt: now.Add(-1 * time.Minute),
			LastSuccessAt: now.Add(-1 * time.Minute),
			NextRunAt:     now.Add(59 * time.Minute),
		},
		{
			Repo: "repo-b", FileGlob: "values.yaml", Field: "image.tag",
			LastAttemptAt: now.Add(-5 * time.Minute),
			LastError:     "clone failed: connection refused",
			NextRunAt:     now.Add(55 * time.Minute),
		},
	}

	got := statusChip(snapshot, now)

	if got.Status != statusError {
		t.Errorf("Status = %q, want %q", got.Status, statusError)
	}
	if got.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", got.ErrorCount)
	}
	if got.ErrorText != "1 tracker failing" {
		t.Errorf("ErrorText = %q, want %q", got.ErrorText, "1 tracker failing")
	}
	if strings.Contains(got.ErrorText, "connection refused") {
		t.Errorf("ErrorText leaked raw LastError text: %q", got.ErrorText)
	}
}

// TestStatusChip_NextRunDerivation_PicksSoonestAcrossTrackers verifies R9's
// aggregate surfaced at R11: the chip's "next poll" is the soonest NextRunAt
// among ALL trackers, not necessarily the one with the most recent last
// attempt — an operator cares about when the next poll of any kind fires.
func TestStatusChip_NextRunDerivation_PicksSoonestAcrossTrackers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	snapshot := []pollstatus.TrackerStatus{
		{
			// Most recently attempted, but its next run is far away.
			Repo: "repo-a", FileGlob: "Chart.yaml", Field: "version",
			LastAttemptAt: now.Add(-1 * time.Minute),
			LastSuccessAt: now.Add(-1 * time.Minute),
			NextRunAt:     now.Add(3 * time.Hour),
		},
		{
			// Attempted longer ago, but its next run is the soonest.
			Repo: "repo-b", FileGlob: "values.yaml", Field: "image.tag",
			LastAttemptAt: now.Add(-30 * time.Minute),
			LastSuccessAt: now.Add(-30 * time.Minute),
			NextRunAt:     now.Add(5 * time.Minute),
		},
	}

	got := statusChip(snapshot, now)

	if got.NextPollText != "Next poll: in 5 minutes" {
		t.Errorf("NextPollText = %q, want %q (soonest NextRunAt across trackers)", got.NextPollText, "Next poll: in 5 minutes")
	}
	if got.LastPollText != "Last poll: 1 minute ago" {
		t.Errorf("LastPollText = %q, want %q (most recent LastAttemptAt across trackers)", got.LastPollText, "Last poll: 1 minute ago")
	}
}
