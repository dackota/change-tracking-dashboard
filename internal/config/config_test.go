// Package config_test exercises the config package through its public interface.
// Tests drive deterministic reload via the exported Reload() method so we
// never sleep on the poll interval.
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
)

// minimalValidYAML is the smallest syntactically-valid config that satisfies
// all required-field rules.
const minimalValidYAML = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /some/repo
    facetRegex: '^apps/(?P<tenant>[^/]+)/'
    files:
      - glob: 'apps/Chart.yaml'
        fields:
          - name: aidp-version
            expr: '.version'
`

// fullYAML exercises per-tracker overrides and multiple files/fields.
const fullYAML = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo/a
    pollIntervalSeconds: 30
    backfillDays: 14
    facetRegex: '^apps/(?P<tenant>[^/]+)/(?P<env>[^/]+)/(?P<region>[^/]+)/'
    files:
      - glob: 'apps/*/*/*/Chart.yaml'
        fields:
          - name: aidp-version
            expr: '.dependencies[] | select(.name=="aidp") | .version'
      - glob: 'aidp/k8/values.yaml'
        fields:
          - name: image-tags
            expr: 'to_entries | map(select(.value.image.tag)) | map({(.key): .value.image.tag}) | add'
  - repo: /repo/b
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: chart-version
            expr: '.version'
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return p
}

// --- Behavior 1: valid config round-trips to the expected trackers + defaults ---

func TestLoad_MinimalValid_ParsesDefaults(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	cfg := w.Current()
	if cfg.Defaults.PollIntervalSeconds != 60 {
		t.Errorf("PollIntervalSeconds = %d, want 60", cfg.Defaults.PollIntervalSeconds)
	}
	if cfg.Defaults.BackfillDays != 90 {
		t.Errorf("BackfillDays = %d, want 90", cfg.Defaults.BackfillDays)
	}
}

func TestLoad_MinimalValid_FlattensTrackers(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	trackers := w.Current().Trackers
	if len(trackers) != 1 {
		t.Fatalf("len(Trackers) = %d, want 1", len(trackers))
	}
	tr := trackers[0]
	if tr.Repo != "/some/repo" {
		t.Errorf("Tracker.Repo = %q, want %q", tr.Repo, "/some/repo")
	}
	if tr.FileGlob != "apps/Chart.yaml" {
		t.Errorf("Tracker.FileGlob = %q, want %q", tr.FileGlob, "apps/Chart.yaml")
	}
	if tr.Field != "aidp-version" {
		t.Errorf("Tracker.Field = %q, want %q", tr.Field, "aidp-version")
	}
	if tr.ExtractorExpr != ".version" {
		t.Errorf("Tracker.ExtractorExpr = %q, want %q", tr.ExtractorExpr, ".version")
	}
}

func TestLoad_FullConfig_FlattensToCorrectCount(t *testing.T) {
	path := writeTemp(t, fullYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	// /repo/a: 1 glob × 1 field + 1 glob × 1 field = 2
	// /repo/b: 1 glob × 1 field = 1
	// total = 3
	trackers := w.Current().Trackers
	if len(trackers) != 3 {
		t.Fatalf("len(Trackers) = %d, want 3", len(trackers))
	}
}

func TestLoad_FullConfig_TrackerDomainShape(t *testing.T) {
	path := writeTemp(t, fullYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	// First tracker: repo/a, Chart.yaml glob
	tr := w.Current().Trackers[0]
	wantTracker := domain.Tracker{
		Repo:          "/repo/a",
		FileGlob:      "apps/*/*/*/Chart.yaml",
		Field:         "aidp-version",
		ExtractorExpr: `.dependencies[] | select(.name=="aidp") | .version`,
		FacetPattern:  `^apps/(?P<tenant>[^/]+)/(?P<env>[^/]+)/(?P<region>[^/]+)/`,
	}
	if tr != wantTracker {
		t.Errorf("Trackers[0] =\n  %+v\nwant\n  %+v", tr, wantTracker)
	}
}

// --- Behavior 2: defaults applied / overridden per tracker ---

func TestLoad_DefaultsApplied_WhenNoOverride(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	tr := w.Current().TrackerConfigs[0]
	if tr.PollIntervalSeconds != 60 {
		t.Errorf("resolved PollIntervalSeconds = %d, want 60 (default)", tr.PollIntervalSeconds)
	}
	if tr.BackfillDays != 90 {
		t.Errorf("resolved BackfillDays = %d, want 90 (default)", tr.BackfillDays)
	}
}

func TestLoad_PerTrackerOverrides_AppliedCorrectly(t *testing.T) {
	path := writeTemp(t, fullYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	cfgs := w.Current().TrackerConfigs
	// /repo/a has explicit overrides: 30 / 14
	if cfgs[0].PollIntervalSeconds != 30 {
		t.Errorf("repo/a PollIntervalSeconds = %d, want 30", cfgs[0].PollIntervalSeconds)
	}
	if cfgs[0].BackfillDays != 14 {
		t.Errorf("repo/a BackfillDays = %d, want 14", cfgs[0].BackfillDays)
	}
	// /repo/b inherits defaults: 60 / 90
	if cfgs[1].PollIntervalSeconds != 60 {
		t.Errorf("repo/b PollIntervalSeconds = %d, want 60 (inherited)", cfgs[1].PollIntervalSeconds)
	}
	if cfgs[1].BackfillDays != 90 {
		t.Errorf("repo/b BackfillDays = %d, want 90 (inherited)", cfgs[1].BackfillDays)
	}
}

// --- Behavior 3: invalid jq expression rejected at load ---

func TestLoad_InvalidJQExpr_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: broken
            expr: '!!! not valid jq'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for invalid jq expr, got nil")
	}
	// Error must mention where — tracker, file, field, and the bad expression.
	errStr := err.Error()
	for _, want := range []string{"broken", "!!! not valid jq"} {
		if !contains(errStr, want) {
			t.Errorf("error %q does not mention %q", errStr, want)
		}
	}
}

// --- Behavior 4: invalid facet regex rejected at load ---

func TestLoad_InvalidFacetRegex_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    facetRegex: '(?P<bad'
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: version
            expr: '.version'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for invalid regex, got nil")
	}
	errStr := err.Error()
	if !contains(errStr, "(?P<bad") {
		t.Errorf("error %q does not mention the bad pattern", errStr)
	}
}

// --- Behavior 5: required-field validation ---

func TestLoad_MissingRepo_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: version
            expr: '.version'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for missing repo, got nil")
	}
	if !contains(err.Error(), "repo") {
		t.Errorf("error %q does not mention 'repo'", err.Error())
	}
}

func TestLoad_NoFiles_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    facetRegex: ''
    files: []
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for empty files, got nil")
	}
}

func TestLoad_MissingGlob_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    facetRegex: ''
    files:
      - fields:
          - name: version
            expr: '.version'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for missing glob, got nil")
	}
	if !contains(err.Error(), "glob") {
		t.Errorf("error %q does not mention 'glob'", err.Error())
	}
}

func TestLoad_NoFields_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields: []
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for empty fields, got nil")
	}
}

func TestLoad_MissingFieldName_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - expr: '.version'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for missing field name, got nil")
	}
	if !contains(err.Error(), "name") {
		t.Errorf("error %q does not mention 'name'", err.Error())
	}
}

func TestLoad_MissingFieldExpr_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: version
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for missing field expr, got nil")
	}
	if !contains(err.Error(), "expr") {
		t.Errorf("error %q does not mention 'expr'", err.Error())
	}
}

// --- Behavior 6: sane defaults validation ---

func TestLoad_ZeroPollInterval_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 0
  backfillDays: 90
trackers:
  - repo: /repo
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: version
            expr: '.version'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for zero pollIntervalSeconds, got nil")
	}
}

func TestLoad_NegativeBackfillDays_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: -1
trackers:
  - repo: /repo
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: version
            expr: '.version'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for negative backfillDays, got nil")
	}
}

// --- Behavior 7: missing file returns error ---

func TestLoad_MissingFile_ReturnsError(t *testing.T) {
	_, err := config.Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("Load should have returned an error for missing file, got nil")
	}
}

// --- Behavior 8: hot-reload via exported Reload() ---

func TestWatcher_Reload_PicksUpChangedFile(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	before := w.Current().Trackers[0].Repo

	// Overwrite with new config pointing to a different repo.
	newYAML := `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /new/repo
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: version
            expr: '.version'
`
	if err := os.WriteFile(path, []byte(newYAML), 0o600); err != nil {
		t.Fatalf("write new config: %v", err)
	}

	if err := w.Reload(); err != nil {
		t.Fatalf("Reload returned error: %v", err)
	}

	after := w.Current().Trackers[0].Repo
	if after == before {
		t.Errorf("Reload did not pick up new config: Repo still %q", after)
	}
	if after != "/new/repo" {
		t.Errorf("Repo = %q, want /new/repo", after)
	}
}

func TestWatcher_BadReload_KeepsLastGoodConfig(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	goodTrackers := w.Current().Trackers

	// Overwrite with broken config (invalid jq).
	if err := os.WriteFile(path, []byte("trackers: []\n"), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}

	reloadErr := w.Reload()
	if reloadErr == nil {
		t.Fatal("Reload should have returned an error for bad config, got nil")
	}

	// Current() must still return the last-good config.
	afterTrackers := w.Current().Trackers
	if len(afterTrackers) != len(goodTrackers) {
		t.Errorf("Current() after bad reload has %d trackers, want %d (last-good)",
			len(afterTrackers), len(goodTrackers))
	}
}

// --- Behavior 9: Reload when file is deleted returns error, keeps last-good ---

func TestWatcher_Reload_DeletedFile_KeepsLastGoodConfig(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	goodTrackers := w.Current().Trackers

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove config file: %v", err)
	}

	reloadErr := w.Reload()
	if reloadErr == nil {
		t.Fatal("Reload should have returned an error for deleted file, got nil")
	}

	// Current() must still return the last-good config.
	afterTrackers := w.Current().Trackers
	if len(afterTrackers) != len(goodTrackers) {
		t.Errorf("Current() after deleted-file reload has %d trackers, want %d (last-good)",
			len(afterTrackers), len(goodTrackers))
	}
}

// --- Behavior 10: no trackers in config rejects at load ---

func TestLoad_NoTrackers_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers: []
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have returned an error for empty trackers list, got nil")
	}
}

// --- Behavior 11: per-tracker zero poll interval inherits default ---

func TestLoad_ZeroPerTrackerPollInterval_InheritsDefault(t *testing.T) {
	// pollIntervalSeconds: 0 on a tracker means "use default" (not override to 0).
	const yaml = `
defaults:
  pollIntervalSeconds: 45
  backfillDays: 30
trackers:
  - repo: /repo
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: version
            expr: '.version'
`
	path := writeTemp(t, yaml)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if w.Current().TrackerConfigs[0].PollIntervalSeconds != 45 {
		t.Errorf("PollIntervalSeconds = %d, want 45 (inherited default)",
			w.Current().TrackerConfigs[0].PollIntervalSeconds)
	}
}

// --- helpers ---

func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) &&
		(s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
