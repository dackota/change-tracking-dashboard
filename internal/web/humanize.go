// Package web (this file): small pure formatting helpers for the "last
// change" KPI tile — a relative phrase ("2 hours ago") paired with a precise
// absolute timestamp (R6). Both take time.Time values explicitly (never call
// time.Now() themselves) so they stay pure and independently testable.
package web

import (
	"fmt"
	"time"
)

// noChangesLabel is the sensible empty value for the "last change" KPI tile
// when no Changeset has ever been recorded (R6) — including when the bounded
// KPI store read itself failed and degraded to the zero Metrics (R21).
const noChangesLabel = "No changes yet"

// absoluteTimestampLayout formats a last-change instant as e.g. "Jul 5,
// 16:26" — always in UTC so the rendered value is deterministic regardless
// of server timezone.
const absoluteTimestampLayout = "Jan 2, 15:04"

// humanizeRelative renders the elapsed time between t and now as a short,
// human-readable phrase: "just now", "5 minutes ago", "3 hours ago", "2 days
// ago". A zero t (no Change ever recorded) yields noChangesLabel rather than
// a nonsensical multi-decade duration. A t after now (clock skew) is clamped
// to "just now" rather than a negative duration.
func humanizeRelative(t, now time.Time) string {
	if t.IsZero() {
		return noChangesLabel
	}

	elapsed := now.Sub(t)
	if elapsed < 0 {
		elapsed = 0
	}

	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return pluralUnit(int(elapsed/time.Minute), "minute") + " ago"
	case elapsed < 24*time.Hour:
		return pluralUnit(int(elapsed/time.Hour), "hour") + " ago"
	default:
		return pluralUnit(int(elapsed/(24*time.Hour)), "day") + " ago"
	}
}

// humanizeUntil renders the time remaining between now and a future instant
// t as a short, human-readable phrase: "due now", "in 5 minutes", "in 3
// hours", "in 2 days". A zero t (no scheduled run known) yields "unknown"
// rather than a nonsensical multi-decade duration. A t at or before now is
// rendered as "due now" rather than a negative duration.
func humanizeUntil(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	remaining := t.Sub(now)
	if remaining <= 0 {
		return "due now"
	}

	switch {
	case remaining < time.Minute:
		return "in under a minute"
	case remaining < time.Hour:
		return "in " + pluralUnit(int(remaining/time.Minute), "minute")
	case remaining < 24*time.Hour:
		return "in " + pluralUnit(int(remaining/time.Hour), "hour")
	default:
		return "in " + pluralUnit(int(remaining/(24*time.Hour)), "day")
	}
}

// formatAbsolute renders t as an absolute UTC timestamp, paired with
// humanizeRelative's phrase so the tile carries both an at-a-glance label and
// a precise value (R6). A zero t yields "".
func formatAbsolute(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(absoluteTimestampLayout)
}

// pluralUnit renders n alongside unit, pluralized ("1 minute" vs "5
// minutes").
func pluralUnit(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
