package web

import (
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
)

// TestStatusChip_NoTrackersEverPolled_ReturnsUnknownStatus verifies R11's
// "quiet vs. broken" baseline: an empty snapshot (the shape Snapshot()
// returns before the scheduler has recorded a single poll attempt) must
// render an explicit "never polled" state, not a nonsensical zero-time
// phrase.
func TestStatusChip_NoTrackersEverPolled_ReturnsUnknownStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	got := statusChip(nil, nil, now)

	want := statusChipView{
		Status:       statusUnknown,
		LastPollText: "Last poll: never",
		NextPollText: "Next poll: —",
	}
	if got != want {
		t.Errorf("statusChip(nil, nil, now) = %+v, want %+v", got, want)
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

	got := statusChip(snapshot, nil, now)

	want := statusChipView{
		Status:       statusOK,
		LastPollText: "Last poll: 2 minutes ago",
		NextPollText: "Next poll: in 58 minutes",
	}
	if got != want {
		t.Errorf("statusChip(snapshot, nil, now) = %+v, want %+v", got, want)
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

	got := statusChip(snapshot, nil, now)

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

	got := statusChip(snapshot, nil, now)

	if got.NextPollText != "Next poll: in 5 minutes" {
		t.Errorf("NextPollText = %q, want %q (soonest NextRunAt across trackers)", got.NextPollText, "Next poll: in 5 minutes")
	}
	if got.LastPollText != "Last poll: 1 minute ago" {
		t.Errorf("LastPollText = %q, want %q (most recent LastAttemptAt across trackers)", got.LastPollText, "Last poll: 1 minute ago")
	}
}

// TestStatusChip_ExtractFailures_SurfacedIndependentlyOfPollOutcome verifies
// acceptance criterion 9: an engine's field-extraction failures (e.g. a
// malformed HCL file, skipped rather than failing the whole poll — see
// pollstatus.Registry.RecordExtractFailure) surface on the chip's
// ExtractFailureText even when every tracker's poll attempt itself
// succeeded (Status stays "ok") and even when no tracker has ever been
// polled at all (Status stays "unknown") — extraction failures are a
// distinct signal from poll-attempt failures (ErrorText/Status), never
// conflated with them.
func TestStatusChip_ExtractFailures_SurfacedIndependentlyOfPollOutcome(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	extractFailures := map[string]int64{"hcl": 2}

	t.Run("alongside an all-success snapshot", func(t *testing.T) {
		t.Parallel()
		snapshot := []pollstatus.TrackerStatus{
			{
				Repo: "repo-a", FileGlob: "*.tf", Field: "provider_version",
				LastAttemptAt: now.Add(-1 * time.Minute),
				LastSuccessAt: now.Add(-1 * time.Minute),
				NextRunAt:     now.Add(59 * time.Minute),
			},
		}

		got := statusChip(snapshot, extractFailures, now)

		if got.Status != statusOK {
			t.Errorf("Status = %q, want %q (extract failures don't flip poll-attempt status)", got.Status, statusOK)
		}
		if got.ExtractFailureText != "2 hcl parse failures" {
			t.Errorf("ExtractFailureText = %q, want %q", got.ExtractFailureText, "2 hcl parse failures")
		}
	})

	t.Run("alongside an empty (never-polled) snapshot", func(t *testing.T) {
		t.Parallel()

		got := statusChip(nil, extractFailures, now)

		if got.Status != statusUnknown {
			t.Errorf("Status = %q, want %q", got.Status, statusUnknown)
		}
		if got.ExtractFailureText != "2 hcl parse failures" {
			t.Errorf("ExtractFailureText = %q, want %q", got.ExtractFailureText, "2 hcl parse failures")
		}
	})
}

// TestFormatExtractFailureText_NoFailures_ReturnsEmpty verifies the
// poll-health chip carries no extract-failure phrase before any engine has
// ever failed to extract a field — nil and empty maps must render
// identically (both are "no failure yet", never a phantom "0 failures"
// phrase).
func TestFormatExtractFailureText_NoFailures_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	for name, counts := range map[string]map[string]int64{
		"nil map":   nil,
		"empty map": {},
	} {
		if got := formatExtractFailureText(counts); got != "" {
			t.Errorf("%s: formatExtractFailureText(%v) = %q, want \"\"", name, counts, got)
		}
	}
}

// TestFormatExtractFailureText_SingleEngine_RendersSingularOrPlural verifies
// the count-driven singular/plural phrasing for one engine's failures,
// satisfying acceptance criterion 9's "HCL parse-failure counts must appear
// on the poll-health/status surface".
func TestFormatExtractFailureText_SingleEngine_RendersSingularOrPlural(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		counts map[string]int64
		want   string
	}{
		{"singular failure", map[string]int64{"hcl": 1}, "1 hcl parse failure"},
		{"plural failures", map[string]int64{"hcl": 3}, "3 hcl parse failures"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatExtractFailureText(tt.counts); got != tt.want {
				t.Errorf("formatExtractFailureText(%v) = %q, want %q", tt.counts, got, tt.want)
			}
		})
	}
}

// TestFormatExtractFailureText_MultipleEngines_SortsDeterministically
// verifies engines are always rendered in the same (alphabetical) order
// regardless of map iteration order, so the chip's text never flickers
// between requests for the same underlying counts — a real hazard since Go
// map iteration order is randomized.
func TestFormatExtractFailureText_MultipleEngines_SortsDeterministically(t *testing.T) {
	t.Parallel()

	counts := map[string]int64{"jq": 1, "hcl": 2}
	want := "2 hcl parse failures, 1 jq parse failure"

	for i := 0; i < 20; i++ {
		if got := formatExtractFailureText(counts); got != want {
			t.Errorf("formatExtractFailureText(%v) = %q, want %q", counts, got, want)
		}
	}
}
