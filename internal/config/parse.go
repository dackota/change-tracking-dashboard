// parse.go handles YAML unmarshalling + validation + flattening. These are pure
// functions except readConfigFile (called from parseFile), which reads the
// size-capped config file from disk.
package config

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxConfigBytes caps how much of the config file is read into memory, both on
// load and on every hot-reload tick. 1 MiB matches the Kubernetes ConfigMap
// per-key size limit, so a correctly-sized ConfigMap always fits; a misconfigured
// or replaced multi-MB file is rejected rather than read in full.
const maxConfigBytes = 1 << 20 // 1 MiB

// rawConfig is the direct YAML deserialization target.
type rawConfig struct {
	Defaults Defaults     `yaml:"defaults"`
	Trackers []TrackerRaw `yaml:"trackers"`
}

// readConfigFile reads at most maxConfigBytes from path, rejecting anything
// larger rather than allocating it. Shared by parseFile and the watcher's
// change-detection hash so both honor the same cap.
func readConfigFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	defer f.Close()

	// Read one byte past the cap so we can detect an over-limit file.
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("config: file %q exceeds the %d-byte limit", path, maxConfigBytes)
	}
	return data, nil
}

// parseFile reads path (size-capped), unmarshals the YAML, validates, and flattens.
// It is called both from Load and from Watcher.Reload.
func parseFile(path string) (*Config, error) {
	data, err := readConfigFile(path)
	if err != nil {
		return nil, err
	}
	return parseBytes(data)
}

// parseBytes unmarshals, validates, and flattens raw YAML bytes. It is shared by
// parseFile and by in-package tests that supply literal YAML without a file.
func parseBytes(data []byte) (*Config, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: unmarshal yaml: %w", err)
	}

	if err := validateDefaults(raw.Defaults); err != nil {
		return nil, err
	}

	if len(raw.Trackers) == 0 {
		return nil, fmt.Errorf("config: at least one tracker is required")
	}

	resolved := make([]ResolvedTracker, 0, len(raw.Trackers))
	var domainTrackers []domainTracker

	for i, tr := range raw.Trackers {
		rt, err := resolveTracker(i, tr, raw.Defaults)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, rt)

		flat, err := flattenTracker(i, tr, rt)
		if err != nil {
			return nil, err
		}
		domainTrackers = append(domainTrackers, flat...)
	}

	return &Config{
		Defaults:       raw.Defaults,
		TrackerConfigs: resolved,
		Trackers:       domainTrackers,
	}, nil
}

// domainTracker aliases domain.Tracker for use in parse.go without re-importing
// the domain package (it is imported via validate.go's domainTrackerType).
type domainTracker = domainTrackerType

// Upper bounds on the time-valued config fields. They keep the downstream
// time.Duration arithmetic well within int64 — e.g. days*24h overflows ~292
// years out, after which a negative duration would silently skip the backfill
// (notBefore in the future). The caps are far above any realistic setting, so
// they only reject absurd or typo'd values, failing fast with a clear error.
const (
	maxBackfillDays        = 36500    // 100 years
	maxPollIntervalSeconds = 31622400 // ~366 days
)

// validateDefaults checks that the global defaults are sensible.
func validateDefaults(d Defaults) error {
	if d.PollIntervalSeconds <= 0 || d.PollIntervalSeconds > maxPollIntervalSeconds {
		return fmt.Errorf("config: defaults.pollIntervalSeconds must be between 1 and %d, got %d", maxPollIntervalSeconds, d.PollIntervalSeconds)
	}
	if d.BackfillDays < 0 || d.BackfillDays > maxBackfillDays {
		return fmt.Errorf("config: defaults.backfillDays must be between 0 and %d, got %d", maxBackfillDays, d.BackfillDays)
	}
	return nil
}

// resolveTracker applies defaults to a raw tracker, validates the result,
// and compiles the facetRegex.
func resolveTracker(idx int, tr TrackerRaw, defaults Defaults) (ResolvedTracker, error) {
	if tr.Repo == "" {
		return ResolvedTracker{}, fmt.Errorf("config: tracker[%d]: repo is required", idx)
	}
	if strings.HasPrefix(tr.Repo, "http://") {
		return ResolvedTracker{}, fmt.Errorf(
			"config: tracker[%d]: tracker repo %q must use https:// (plaintext http:// is not allowed)",
			idx, tr.Repo)
	}
	if len(tr.Files) == 0 {
		return ResolvedTracker{}, fmt.Errorf("config: tracker[%d] (repo=%q): at least one file is required", idx, tr.Repo)
	}

	poll := tr.PollIntervalSecondsOverride
	if poll == 0 {
		poll = defaults.PollIntervalSeconds
	}
	if poll <= 0 || poll > maxPollIntervalSeconds {
		return ResolvedTracker{}, fmt.Errorf("config: tracker[%d] (repo=%q): resolved pollIntervalSeconds must be between 1 and %d, got %d", idx, tr.Repo, maxPollIntervalSeconds, poll)
	}

	backfill := defaults.BackfillDays
	if tr.BackfillDaysOverride != nil {
		backfill = *tr.BackfillDaysOverride
	}
	if backfill < 0 || backfill > maxBackfillDays {
		return ResolvedTracker{}, fmt.Errorf("config: tracker[%d] (repo=%q): resolved backfillDays must be between 0 and %d, got %d", idx, tr.Repo, maxBackfillDays, backfill)
	}

	// Validate facetRegex by compiling it (using facet package's compile logic).
	if err := validateFacetRegex(idx, tr.Repo, tr.FacetRegex); err != nil {
		return ResolvedTracker{}, err
	}

	// Validate engine: only "" (defaults to jq) and "jq" are legal today.
	if err := validateEngine(idx, tr.Repo, tr.Engine); err != nil {
		return ResolvedTracker{}, err
	}

	return ResolvedTracker{
		Repo:                tr.Repo,
		FacetRegex:          tr.FacetRegex,
		Engine:              tr.Engine,
		Files:               tr.Files,
		PollIntervalSeconds: poll,
		BackfillDays:        backfill,
	}, nil
}

// flattenTracker produces one domain.Tracker per (file-glob × field) entry,
// validating jq expressions along the way. The resolved per-tracker cadence
// and backfill window are carried onto every flattened tracker.
func flattenTracker(trackerIdx int, tr TrackerRaw, rt ResolvedTracker) ([]domainTracker, error) {
	var out []domainTracker

	for fileIdx, f := range tr.Files {
		if f.Glob == "" {
			return nil, fmt.Errorf("config: tracker[%d] (repo=%q), file[%d]: glob is required", trackerIdx, tr.Repo, fileIdx)
		}
		if len(f.Fields) == 0 {
			return nil, fmt.Errorf("config: tracker[%d] (repo=%q), file[%d] (glob=%q): at least one field is required", trackerIdx, tr.Repo, fileIdx, f.Glob)
		}

		for fieldIdx, field := range f.Fields {
			if field.Name == "" {
				return nil, fmt.Errorf("config: tracker[%d] (repo=%q), file[%d] (glob=%q), field[%d]: name is required", trackerIdx, tr.Repo, fileIdx, f.Glob, fieldIdx)
			}
			if field.Expr == "" {
				return nil, fmt.Errorf("config: tracker[%d] (repo=%q), file[%d] (glob=%q), field[%d] (name=%q): expr is required", trackerIdx, tr.Repo, fileIdx, f.Glob, fieldIdx, field.Name)
			}
			if err := validateJQExpr(trackerIdx, tr.Repo, fileIdx, f.Glob, fieldIdx, field.Name, field.Expr); err != nil {
				return nil, err
			}

			out = append(out, domainTrackerType{
				Repo:                tr.Repo,
				FileGlob:            f.Glob,
				Field:               field.Name,
				ExtractorExpr:       field.Expr,
				FacetPattern:        tr.FacetRegex,
				Engine:              rt.Engine,
				PollIntervalSeconds: rt.PollIntervalSeconds,
				BackfillDays:        rt.BackfillDays,
			})
		}
	}

	return out, nil
}
