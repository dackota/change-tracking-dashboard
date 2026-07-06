package web

import (
	"testing"
	"time"
)

// TestHumanizeRelative covers the bucketing boundaries humanizeRelative must
// get right: the empty-value case (zero t), the "just now" floor, singular
// vs. plural units at each bucket, the minute/hour/day bucket transitions,
// and a t after now (clock skew) clamping to "just now" instead of going
// negative.
func TestHumanizeRelative(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero time", time.Time{}, noChangesLabel},
		{"30 seconds ago", now.Add(-30 * time.Second), "just now"},
		{"exactly now", now, "just now"},
		{"1 minute ago (singular)", now.Add(-1 * time.Minute), "1 minute ago"},
		{"5 minutes ago (plural)", now.Add(-5 * time.Minute), "5 minutes ago"},
		{"59 minutes ago", now.Add(-59 * time.Minute), "59 minutes ago"},
		{"1 hour ago (singular)", now.Add(-1 * time.Hour), "1 hour ago"},
		{"3 hours ago (plural)", now.Add(-3 * time.Hour), "3 hours ago"},
		{"23 hours ago", now.Add(-23 * time.Hour), "23 hours ago"},
		{"1 day ago (singular)", now.Add(-24 * time.Hour), "1 day ago"},
		{"2 days ago (plural)", now.Add(-48 * time.Hour), "2 days ago"},
		{"30 days ago", now.Add(-30 * 24 * time.Hour), "30 days ago"},
		{"clock skew: t after now", now.Add(1 * time.Hour), "just now"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanizeRelative(tc.t, now)
			if got != tc.want {
				t.Errorf("humanizeRelative(%v, %v) = %q, want %q", tc.t, now, got, tc.want)
			}
		})
	}
}

// TestFormatAbsolute covers the paired absolute-timestamp helper: a non-zero
// time formats deterministically in UTC regardless of its input location,
// and a zero time (no data) yields the empty string rather than a
// zero-value timestamp like "0001-01-01".
func TestFormatAbsolute(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*60*60)
	inUTCMinus5 := time.Date(2026, 7, 5, 11, 26, 0, 0, loc)

	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero time yields empty string", time.Time{}, ""},
		{"formats in UTC regardless of input location", inUTCMinus5, "Jul 5, 16:26"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatAbsolute(tc.t)
			if got != tc.want {
				t.Errorf("formatAbsolute(%v) = %q, want %q", tc.t, got, tc.want)
			}
		})
	}
}
