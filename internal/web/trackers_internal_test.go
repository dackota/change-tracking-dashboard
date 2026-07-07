package web

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/pollstatus"
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

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	got := buildTrackersView(cfg, nil, now)

	want := []trackerView{
		{
			Repo:           "github.com/example/apps",
			FileGlobs:      []string{"values.yaml", "charts/*/Chart.yaml"},
			TrackedFields:  []string{"image.tag", "replicaCount", "version"},
			PollCadence:    "every 1m0s",
			BackfillWindow: "7 days",
			LastSuccess:    "never",
			NextRun:        "unknown",
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

	got := buildTrackersView(cfg, nil, time.Now())
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

	got := buildTrackersView(&config.Config{}, nil, time.Now())
	if got == nil {
		t.Fatal("buildTrackersView(&config.Config{}, nil, now) = nil, want a non-nil empty slice")
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

	got := buildTrackersView(nil, nil, time.Now())
	if got == nil {
		t.Fatal("buildTrackersView(nil, nil, now) = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// TestBuildTrackersView_PerTrackerStatusColumns_MapsPollHealth verifies R12:
// each row's LastSuccess/LastError/NextRun columns are populated from the
// pollstatus entries matching that row's repo — aggregated the same way as
// the header chip (most recent success, soonest next run, most recent
// error) — while a repo with no matching pollstatus entry (never polled)
// degrades to "never"/""/"unknown" rather than a zero-time artifact.
func TestBuildTrackersView_PerTrackerStatusColumns_MapsPollHealth(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		TrackerConfigs: []config.ResolvedTracker{
			{Repo: "repo-healthy", PollIntervalSeconds: 60},
			{Repo: "repo-failing", PollIntervalSeconds: 60},
			{Repo: "repo-never-polled", PollIntervalSeconds: 60},
		},
	}
	snapshot := []pollstatus.TrackerStatus{
		{
			Repo: "repo-healthy", FileGlob: "Chart.yaml", Field: "version",
			LastAttemptAt: now.Add(-2 * time.Minute),
			LastSuccessAt: now.Add(-2 * time.Minute),
			NextRunAt:     now.Add(58 * time.Minute),
		},
		{
			Repo: "repo-failing", FileGlob: "values.yaml", Field: "image.tag",
			LastAttemptAt: now.Add(-1 * time.Minute),
			LastError:     "clone failed: connection refused",
			NextRunAt:     now.Add(59 * time.Minute),
		},
	}

	got := buildTrackersView(cfg, snapshot, now)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}

	byRepo := make(map[string]trackerView, len(got))
	for _, tv := range got {
		byRepo[tv.Repo] = tv
	}

	healthy := byRepo["repo-healthy"]
	if healthy.LastSuccess != "2 minutes ago" {
		t.Errorf("repo-healthy LastSuccess = %q, want %q", healthy.LastSuccess, "2 minutes ago")
	}
	if healthy.LastError != "" {
		t.Errorf("repo-healthy LastError = %q, want empty", healthy.LastError)
	}
	if healthy.NextRun != "in 58 minutes" {
		t.Errorf("repo-healthy NextRun = %q, want %q", healthy.NextRun, "in 58 minutes")
	}

	failing := byRepo["repo-failing"]
	if failing.LastSuccess != "never" {
		t.Errorf("repo-failing LastSuccess = %q, want %q", failing.LastSuccess, "never")
	}
	if failing.LastError != "clone failed: connection refused" {
		t.Errorf("repo-failing LastError = %q, want %q", failing.LastError, "clone failed: connection refused")
	}
	if failing.NextRun != "in 59 minutes" {
		t.Errorf("repo-failing NextRun = %q, want %q", failing.NextRun, "in 59 minutes")
	}

	never := byRepo["repo-never-polled"]
	if never.LastSuccess != "never" || never.LastError != "" || never.NextRun != "unknown" {
		t.Errorf("repo-never-polled = %+v, want LastSuccess=never LastError=\"\" NextRun=unknown", never)
	}
}

// TestBuildTrackerStatusView_OversizedLastError_IsTruncated is a hardening
// invariant (bounded rendering — see the project's coding standards): a raw
// Go error string is unbounded in principle (it can embed a subprocess's
// full stderr output). buildTrackerStatusView must cap LastError's rendered
// length rather than propagating an arbitrarily large string into every
// response the Trackers page ever serves.
func TestBuildTrackerStatusView_OversizedLastError_IsTruncated(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	huge := strings.Repeat("x", 5000)
	snapshot := []pollstatus.TrackerStatus{
		{
			Repo: "repo-a", FileGlob: "Chart.yaml", Field: "version",
			LastAttemptAt: now,
			LastError:     huge,
			NextRunAt:     now.Add(time.Minute),
		},
	}

	got := buildTrackerStatusView("repo-a", snapshot, now)

	if len(got.LastError) >= len(huge) {
		t.Errorf("LastError len = %d, want truncated well below the original %d-byte error", len(got.LastError), len(huge))
	}
	if len(got.LastError) > maxPollErrorDisplayLen+len("…") {
		t.Errorf("LastError len = %d, want <= %d (display cap) plus the ellipsis marker", len(got.LastError), maxPollErrorDisplayLen)
	}
}
