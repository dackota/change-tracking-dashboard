package web

import (
	"reflect"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
)

// TestBuildTrackersView_MapsResolvedTrackerFieldsToViewRows verifies R5: a
// config snapshot's TrackerConfigs map to view rows carrying the repo, the
// file globs it tracks, the tracked fields those globs' extractors yield,
// how often it polls, and the backfill window walked on first run.
func TestBuildTrackersView_MapsResolvedTrackerFieldsToViewRows(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		TrackerConfigs: []config.ResolvedTracker{
			{
				Repo: "github.com/example/apps",
				Files: []config.FileConfig{
					{
						Glob: "values.yaml",
						Fields: []config.FieldConfig{
							{Name: "image.tag", Expr: ".image.tag"},
							{Name: "replicaCount", Expr: ".replicaCount"},
						},
					},
					{
						Glob: "charts/*/Chart.yaml",
						Fields: []config.FieldConfig{
							{Name: "version", Expr: ".version"},
						},
					},
				},
				PollIntervalSeconds: 60,
				BackfillDays:        7,
			},
		},
	}

	got := buildTrackersView(cfg)

	want := []trackerView{
		{
			Repo:           "github.com/example/apps",
			FileGlobs:      []string{"values.yaml", "charts/*/Chart.yaml"},
			TrackedFields:  []string{"image.tag", "replicaCount", "version"},
			PollCadence:    "every 1m0s",
			BackfillWindow: "7 days",
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildTrackersView(cfg) =\n%#v\nwant\n%#v", got, want)
	}
}

// TestBuildTrackersView_SingularBackfillDay verifies backfill window
// pluralization: exactly 1 day renders "1 day", not "1 days".
func TestBuildTrackersView_SingularBackfillDay(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		TrackerConfigs: []config.ResolvedTracker{
			{Repo: "r", PollIntervalSeconds: 30, BackfillDays: 1},
		},
	}

	got := buildTrackersView(cfg)
	if len(got) != 1 || got[0].BackfillWindow != "1 day" {
		t.Errorf("BackfillWindow = %q, want %q", got[0].BackfillWindow, "1 day")
	}
}

// TestBuildTrackersView_NoConfiguredTrackers_ReturnsEmptyNotNilSlice verifies
// the degrade-to-empty-state contract (R7): a snapshot with no configured
// trackers yields a non-nil, zero-length slice, so the template's
// {{if .Trackers}} branch is driven by length alone.
func TestBuildTrackersView_NoConfiguredTrackers_ReturnsEmptyNotNilSlice(t *testing.T) {
	t.Parallel()

	got := buildTrackersView(&config.Config{})
	if got == nil {
		t.Fatal("buildTrackersView(&config.Config{}) = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// TestBuildTrackersView_NilConfig_ReturnsEmptyNotNilSlice verifies the
// defensive degrade-to-empty-state contract (R7): a nil config snapshot —
// the shape a degraded/unavailable config read would surface as at this
// seam — never panics and yields the same empty, non-nil slice as an empty
// snapshot.
func TestBuildTrackersView_NilConfig_ReturnsEmptyNotNilSlice(t *testing.T) {
	t.Parallel()

	got := buildTrackersView(nil)
	if got == nil {
		t.Fatal("buildTrackersView(nil) = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}
