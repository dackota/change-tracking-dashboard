// Package web (this file): the poll-health surface (R11, R12) — a pure
// reduction over a pollstatus.Registry snapshot into the header's aggregate
// status chip (statusChip) and the Trackers view's per-tracker status
// columns (buildTrackerStatusView), sharing one aggregation primitive
// (aggregatePollHealth) so the two surfaces can never disagree about what
// "last poll" / "next poll" / "any error" mean. Neither function performs
// I/O or calls time.Now() itself — both take the snapshot and the current
// time explicitly, so they stay pure and independently testable (the PRD's
// named testable seam).
package web

import (
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/pollstatus"
)

// Poll-health status values, shared by statusChipView.Status and (via the
// same constants) any CSS/class selection the templates key off of.
const (
	statusUnknown = "unknown" // no poll has ever been attempted for any tracker in scope
	statusOK      = "ok"      // every tracker in scope last succeeded
	statusError   = "error"   // at least one tracker in scope last failed
)

// PollHealthSnapshot is the seam the web layer depends on to read poll
// status — satisfied directly by *pollstatus.Registry. Defined here, at the
// point of use, per this project's small-interfaces convention: both
// TimelineHandler and TrackersHandler hold one to build the header chip (and
// TrackersHandler also uses it for the per-tracker columns).
type PollHealthSnapshot interface {
	Snapshot() []pollstatus.TrackerStatus
}

// statusChipView is the header's aggregate poll-health chip (R11): how long
// ago the most recent poll attempt across every tracker ran, how soon the
// next one is due, and whether any tracker's last poll attempt failed.
// Ready-to-render text fields keep the template free of formatting logic.
type statusChipView struct {
	// Status is "unknown" (no tracker has ever been polled), "ok" (every
	// tracker's last attempt succeeded), or "error" (at least one failed) —
	// drives which chip style renders.
	Status string
	// LastPollText is a ready-to-render phrase, e.g. "Last poll: 2 minutes
	// ago" or "Last poll: never".
	LastPollText string
	// NextPollText is a ready-to-render phrase, e.g. "Next poll: in 3
	// minutes", "Next poll: due now", or "Next poll: —" when unknown.
	NextPollText string
	// ErrorCount is how many trackers' last poll attempt failed. Zero when
	// Status != "error".
	ErrorCount int
	// ErrorText is a short, plural-aware summary rendered only when
	// ErrorCount > 0, e.g. "1 tracker failing" / "3 trackers failing". It
	// deliberately never echoes a tracker's raw LastError text — that stays
	// scoped to the Trackers view's per-tracker column (R12) to avoid
	// widening the unauthenticated-exposure surface of internal error
	// detail (see pollstatus.TrackerStatus.LastError doc comment).
	ErrorText string
}

// statusChip reduces a pollstatus snapshot to the header chip's aggregate
// (R11). now is threaded through explicitly (never time.Now() here) so the
// relative-phrase computation stays testable against a fixed clock. An empty
// snapshot — the shape Snapshot() returns before any tracker has ever been
// polled — renders an explicit "never polled" state rather than a
// nonsensical zero-time phrase, so a quiet dashboard can be told apart from
// a broken one.
func statusChip(snapshot []pollstatus.TrackerStatus, now time.Time) statusChipView {
	if len(snapshot) == 0 {
		return statusChipView{
			Status:       statusUnknown,
			LastPollText: "Last poll: never",
			NextPollText: "Next poll: —",
		}
	}

	agg := aggregatePollHealth(snapshot)
	view := statusChipView{
		Status:       statusOK,
		LastPollText: "Last poll: " + humanizeRelative(agg.LatestAttempt, now),
		NextPollText: "Next poll: " + humanizeUntil(agg.SoonestNextRun, now),
	}
	if agg.ErrorCount > 0 {
		view.Status = statusError
		view.ErrorCount = agg.ErrorCount
		view.ErrorText = pluralUnit(agg.ErrorCount, "tracker") + " failing"
	}
	return view
}

// pollHealthAggregate is the reduction statusChip and buildTrackerStatusView
// both build on: given any subset of a pollstatus snapshot (the whole thing
// for the header chip, one repo's entries for a Trackers-view row), the most
// recent poll attempt, the most recent success, the soonest next-due time,
// and how many (and which) entries currently carry an error.
type pollHealthAggregate struct {
	LatestAttempt  time.Time // zero when the input is empty
	LatestSuccess  time.Time // zero when none of the input has ever succeeded
	SoonestNextRun time.Time // zero when the input is empty
	ErrorCount     int
	// LatestError is the LastError of whichever failing entry has the most
	// recent LastAttemptAt; "" when ErrorCount == 0.
	LatestError string
}

// aggregatePollHealth reduces statuses into a pollHealthAggregate. Callers
// with an empty slice get the zero pollHealthAggregate (all zero times, no
// errors) — every field is documented as zero-safe by its caller.
func aggregatePollHealth(statuses []pollstatus.TrackerStatus) pollHealthAggregate {
	var agg pollHealthAggregate
	var latestFailingAttempt time.Time

	for i, ts := range statuses {
		if i == 0 || ts.LastAttemptAt.After(agg.LatestAttempt) {
			agg.LatestAttempt = ts.LastAttemptAt
		}
		if ts.LastSuccessAt.After(agg.LatestSuccess) {
			agg.LatestSuccess = ts.LastSuccessAt
		}
		if i == 0 || ts.NextRunAt.Before(agg.SoonestNextRun) {
			agg.SoonestNextRun = ts.NextRunAt
		}
		if ts.LastError != "" {
			agg.ErrorCount++
			if agg.LatestError == "" || ts.LastAttemptAt.After(latestFailingAttempt) {
				latestFailingAttempt = ts.LastAttemptAt
				agg.LatestError = ts.LastError
			}
		}
	}

	return agg
}
