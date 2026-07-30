package store_test

import (
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/store"
)

// twBase is the reference instant for TimeWindow tests.
var twBase = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

// TestTimeWindow_ContainsIsHalfOpen is the core invariant of the whole
// windowing feature: Since is inclusive, AsOf is exclusive. Half-open is what
// makes consecutive windows tile the timeline exactly once, so a poller can
// feed the previous request's AsOf straight back as the next request's Since
// without any timestamp arithmetic. Both boundary instants are asserted
// explicitly because "off by one instant" is invisible in every other test.
func TestTimeWindow_ContainsIsHalfOpen(t *testing.T) {
	t.Parallel()

	w := store.TimeWindow{Since: twBase, AsOf: twBase.Add(time.Hour)}

	if !w.Contains(twBase) {
		t.Error("Contains(Since) = false, want true (the lower bound is inclusive)")
	}
	if w.Contains(twBase.Add(time.Hour)) {
		t.Error("Contains(AsOf) = true, want false (the upper bound is exclusive)")
	}
	if !w.Contains(twBase.Add(30 * time.Minute)) {
		t.Error("Contains(midpoint) = false, want true")
	}
	if w.Contains(twBase.Add(-time.Nanosecond)) {
		t.Error("Contains(just before Since) = true, want false")
	}
}

// TestTimeWindow_ZeroSinceIsUnboundedBelow verifies the backward-compatibility
// invariant: a window with no Since behaves exactly as the AsOf-only bound
// every caller used before this type existed. Any instant, however distant,
// is contained so long as it is strictly before AsOf.
func TestTimeWindow_ZeroSinceIsUnboundedBelow(t *testing.T) {
	t.Parallel()

	w := store.TimeWindow{AsOf: twBase}

	if !w.Contains(twBase.Add(-1000 * time.Hour)) {
		t.Error("Contains(distant past) = false, want true (no lower bound)")
	}
	if !w.Contains(twBase.Add(-time.Nanosecond)) {
		t.Error("Contains(just before AsOf) = false, want true")
	}
	if w.Contains(twBase) {
		t.Error("Contains(AsOf) = true, want false (upper bound stays exclusive)")
	}
}
