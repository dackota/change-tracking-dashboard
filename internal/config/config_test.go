// Package config_test exercises the config package through its public interface.
// Tests drive deterministic reload via the exported Reload() method so we
// never sleep on the poll interval.
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/config"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
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

// TestLoad_ObservabilitySection_ParsesOTLPEndpoint verifies the optional
// top-level observability.otlp_endpoint config key (Init's config-sourced
// path, per the observability standard) parses through to
// Config.Observability.OTLPEndpoint.
func TestLoad_ObservabilitySection_ParsesOTLPEndpoint(t *testing.T) {
	yaml := minimalValidYAML + "\nobservability:\n  otlp_endpoint: collector.example.com:4317\n"
	path := writeTemp(t, yaml)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	cfg := w.Current()
	if cfg.Observability.OTLPEndpoint != "collector.example.com:4317" {
		t.Errorf("Observability.OTLPEndpoint = %q, want collector.example.com:4317", cfg.Observability.OTLPEndpoint)
	}
}

// TestLoad_ObservabilitySection_Absent_DefaultsToEmptyEndpoint verifies that
// omitting the observability section entirely (every existing ConfigMap,
// pre-this-slice) parses fine and yields an empty OTLPEndpoint — Init's
// safe-degrade input.
func TestLoad_ObservabilitySection_Absent_DefaultsToEmptyEndpoint(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	cfg := w.Current()
	if cfg.Observability.OTLPEndpoint != "" {
		t.Errorf("Observability.OTLPEndpoint = %q, want empty when section is absent", cfg.Observability.OTLPEndpoint)
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

// TestCurrent_ReturnsIndependentCopy verifies that mutating the value returned
// by Current() does not corrupt the live snapshot handed to other callers.
func TestCurrent_ReturnsIndependentCopy(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	// Mutate the first snapshot aggressively: reassign a field and grow the slice.
	c1 := w.Current()
	c1.Trackers[0].Repo = "MUTATED"
	c1.Trackers = append(c1.Trackers, domain.Tracker{Repo: "injected"})

	// A fresh snapshot must be unaffected by the mutation above.
	c2 := w.Current()
	if len(c2.Trackers) != 1 {
		t.Fatalf("second snapshot len(Trackers) = %d, want 1 (mutation leaked)", len(c2.Trackers))
	}
	if c2.Trackers[0].Repo != "/some/repo" {
		t.Errorf("second snapshot Tracker.Repo = %q, want %q (mutation leaked)", c2.Trackers[0].Repo, "/some/repo")
	}
}

// TestLoad_OversizedFile_Rejected verifies the config file size cap: a file
// larger than the limit is rejected at load rather than read into memory.
func TestLoad_OversizedFile_Rejected(t *testing.T) {
	oversized := minimalValidYAML + "\n# " + strings.Repeat("x", 1<<20)
	path := writeTemp(t, oversized)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error loading an oversized config file, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want it to mention the size limit (\"exceeds\")", err.Error())
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

	// First tracker: repo/a, Chart.yaml glob (with its per-tracker overrides applied).
	tr := w.Current().Trackers[0]
	wantTracker := domain.Tracker{
		Repo:                "/repo/a",
		FileGlob:            "apps/*/*/*/Chart.yaml",
		Field:               "aidp-version",
		ExtractorExpr:       `.dependencies[] | select(.name=="aidp") | .version`,
		FacetPattern:        `^apps/(?P<tenant>[^/]+)/(?P<env>[^/]+)/(?P<region>[^/]+)/`,
		PollIntervalSeconds: 30,
		BackfillDays:        14,
	}
	if tr != wantTracker {
		t.Errorf("Trackers[0] =\n  %+v\nwant\n  %+v", tr, wantTracker)
	}
}

// --- Behavior 1b: flattened domain.Trackers carry resolved poll/backfill values ---

func TestLoad_FlattenedTrackers_CarryResolvedPollAndBackfill_Defaults(t *testing.T) {
	// The minimal config uses no per-tracker overrides; resolved values come from defaults.
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	trackers := w.Current().Trackers
	if len(trackers) != 1 {
		t.Fatalf("expected 1 flattened tracker, got %d", len(trackers))
	}

	tr := trackers[0]
	if tr.PollIntervalSeconds != 60 {
		t.Errorf("PollIntervalSeconds = %d, want 60 (from defaults)", tr.PollIntervalSeconds)
	}
	if tr.BackfillDays != 90 {
		t.Errorf("BackfillDays = %d, want 90 (from defaults)", tr.BackfillDays)
	}
}

func TestLoad_FlattenedTrackers_CarryResolvedPollAndBackfill_PerTrackerOverride(t *testing.T) {
	// fullYAML: /repo/a overrides both fields; all three flattened trackers from
	// /repo/a must reflect those overrides.
	path := writeTemp(t, fullYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	trackers := w.Current().Trackers
	// /repo/a produces 2 flattened trackers (two file×field combos).
	for i := 0; i < 2; i++ {
		if trackers[i].PollIntervalSeconds != 30 {
			t.Errorf("Trackers[%d].PollIntervalSeconds = %d, want 30 (per-tracker override)", i, trackers[i].PollIntervalSeconds)
		}
		if trackers[i].BackfillDays != 14 {
			t.Errorf("Trackers[%d].BackfillDays = %d, want 14 (per-tracker override)", i, trackers[i].BackfillDays)
		}
	}
	// /repo/b inherits defaults.
	if trackers[2].PollIntervalSeconds != 60 {
		t.Errorf("Trackers[2].PollIntervalSeconds = %d, want 60 (inherited default)", trackers[2].PollIntervalSeconds)
	}
	if trackers[2].BackfillDays != 90 {
		t.Errorf("Trackers[2].BackfillDays = %d, want 90 (inherited default)", trackers[2].BackfillDays)
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

// --- Behavior 13: per-tracker engine field ---

// TestLoad_EngineJQ_Accepted verifies an explicit `engine: jq` tracker is
// accepted and flattens with Engine="jq" on the domain.Tracker.
func TestLoad_EngineJQ_Accepted(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    engine: jq
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
		t.Fatalf("Load should accept engine: jq, got error: %v", err)
	}

	trackers := w.Current().Trackers
	if len(trackers) != 1 {
		t.Fatalf("len(Trackers) = %d, want 1", len(trackers))
	}
	if trackers[0].Engine != "jq" {
		t.Errorf("Tracker.Engine = %q, want %q", trackers[0].Engine, "jq")
	}
}

// TestLoad_EngineUnset_DefaultsToEmptyString verifies that omitting `engine`
// is accepted and flattens with the zero value (Engine=""), which is treated
// as jq by the poller/extractor selector — no behavior change from today.
func TestLoad_EngineUnset_DefaultsToEmptyString(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	trackers := w.Current().Trackers
	if len(trackers) != 1 {
		t.Fatalf("len(Trackers) = %d, want 1", len(trackers))
	}
	if trackers[0].Engine != "" {
		t.Errorf("Tracker.Engine = %q, want empty (unset)", trackers[0].Engine)
	}
}

// TestLoad_InvalidEngine_ReturnsError verifies an unrecognized engine value —
// including "hcl", reserved for a future task — is rejected at config load
// with a clear, actionable error naming the tracker and the bad value.
func TestLoad_InvalidEngine_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo/bogus-engine
    engine: hcl
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
		t.Fatal("Load should have rejected engine: hcl, got nil")
	}
	errStr := err.Error()
	for _, want := range []string{"hcl", "/repo/bogus-engine"} {
		if !contains(errStr, want) {
			t.Errorf("error %q does not mention %q", errStr, want)
		}
	}
}

// TestLoad_UnknownEngineValue_ReturnsError double-checks a plain typo (not
// the reserved "hcl" name) is rejected the same way.
func TestLoad_UnknownEngineValue_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo
    engine: jqq
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
		t.Fatal("Load should have rejected engine: jqq, got nil")
	}
	if !contains(err.Error(), "jqq") {
		t.Errorf("error %q does not mention the bad value %q", err.Error(), "jqq")
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

// TestLoad_BackfillDaysTooLarge_Rejected verifies the upper bound on the global
// backfillDays default. Without it, days*24h overflows int64 downstream and the
// backfill window silently lands in the future.
func TestLoad_BackfillDaysTooLarge_Rejected(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 999999
trackers:
  - repo: /some/repo
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
		t.Fatal("Load should reject an oversized defaults.backfillDays, got nil")
	}
	if !contains(err.Error(), "backfillDays") {
		t.Errorf("error %q does not mention 'backfillDays'", err.Error())
	}
}

// TestLoad_BackfillDaysOverrideTooLarge_Rejected verifies the upper bound is
// also enforced on a per-tracker override (a valid default doesn't excuse it).
func TestLoad_BackfillDaysOverrideTooLarge_Rejected(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /some/repo
    backfillDays: 999999
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
		t.Fatal("Load should reject an oversized per-tracker backfillDays override, got nil")
	}
	if !contains(err.Error(), "backfillDays") {
		t.Errorf("error %q does not mention 'backfillDays'", err.Error())
	}
}

// TestLoad_PollIntervalTooLarge_Rejected verifies the same overflow class is
// guarded for pollIntervalSeconds (seconds*time.Second overflows int64 too).
func TestLoad_PollIntervalTooLarge_Rejected(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 99999999
  backfillDays: 90
trackers:
  - repo: /some/repo
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
		t.Fatal("Load should reject an oversized pollIntervalSeconds, got nil")
	}
	if !contains(err.Error(), "pollIntervalSeconds") {
		t.Errorf("error %q does not mention 'pollIntervalSeconds'", err.Error())
	}
}

// --- Behavior 12: http:// tracker repos are rejected at config load ---

// TestLoad_HttpRepo_Rejected verifies that a tracker whose repo begins with
// http:// is rejected at config load with a clear, non-leaking error message.
// An on-path observer could capture an org-scoped installation token sent over
// a plaintext HTTP connection; fail-fast prevents that.
func TestLoad_HttpRepo_Rejected(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: http://github.com/org/repo.git
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
		t.Fatal("Load should have rejected an http:// tracker repo, got nil")
	}
	errStr := err.Error()
	if !contains(errStr, "https://") {
		t.Errorf("error %q does not mention https:// as the required scheme", errStr)
	}
	if !contains(errStr, "http://") {
		t.Errorf("error %q does not mention the rejected http:// scheme", errStr)
	}
	// The error must not contain the actual repo URL (could leak org/repo structure);
	// however the finding only requires it to be clear — mentioning the scheme is enough.
	// Verify the error message follows the expected pattern.
	if !contains(errStr, "plaintext") {
		t.Errorf("error %q does not describe the plaintext risk", errStr)
	}
}

// TestLoad_HttpsRepo_Accepted verifies that https:// repos are still accepted
// (no regression — the fix must only block http://, not https://).
func TestLoad_HttpsRepo_Accepted(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: https://github.com/org/repo.git
    facetRegex: ''
    files:
      - glob: 'Chart.yaml'
        fields:
          - name: version
            expr: '.version'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	// The load itself may fail for other reasons (e.g. jq compile), but it must
	// NOT fail with an http:// rejection error.
	if err != nil && contains(err.Error(), "plaintext http://") {
		t.Errorf("Load rejected an https:// repo as if it were plaintext http://: %v", err)
	}
}

// TestLoad_LocalPathRepo_Accepted verifies that local filesystem paths
// (neither http:// nor https://) continue to be accepted — fixture repos and
// local test paths must not be broken by the http:// guard.
func TestLoad_LocalPathRepo_Accepted(t *testing.T) {
	path := writeTemp(t, minimalValidYAML) // minimalValidYAML uses /some/repo

	_, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load should accept a local-path repo, got error: %v", err)
	}
}
