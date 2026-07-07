// Package config implements the Config module: parse + validate the tracker
// ConfigMap YAML, flatten it into []domain.Tracker for the poller, and watch
// the mounted file for hot-reload.
//
// The public entry point is Load(path), which returns a *Watcher whose
// Current() method always returns the latest valid snapshot. A background poll
// loop (or an explicit Reload() call in tests) re-reads + re-validates the
// file; a bad reload keeps the last-good config and logs a clear error.
package config

import "github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"

// Defaults holds the global defaults from the ConfigMap that individual
// trackers may override.
type Defaults struct {
	// PollIntervalSeconds is how often to poll each tracker.
	// Must be > 0.
	PollIntervalSeconds int `yaml:"pollIntervalSeconds"`
	// BackfillDays is how many days of git history to walk on first run.
	// Must be >= 0.
	BackfillDays int `yaml:"backfillDays"`
}

// FieldConfig is one field extracted from a file.
type FieldConfig struct {
	Name string `yaml:"name"`
	Expr string `yaml:"expr"`
}

// FileConfig is one glob + its field list.
type FileConfig struct {
	Glob   string        `yaml:"glob"`
	Fields []FieldConfig `yaml:"fields"`
}

// TrackerRaw is the raw YAML shape for a tracker entry, including optional
// per-tracker overrides that shadow the Defaults.
type TrackerRaw struct {
	Repo string `yaml:"repo"`
	// Optional per-tracker overrides; zero means "use default".
	PollIntervalSecondsOverride int    `yaml:"pollIntervalSeconds"`
	BackfillDaysOverride        *int   `yaml:"backfillDays"` // pointer to distinguish 0 from absent
	FacetRegex                  string `yaml:"facetRegex"`
	// Engine selects the FieldExtractor backend for this tracker. Empty
	// defaults to jq (today's only implementation). See extractor.ValidateEngine
	// for the set of legal values.
	Engine string       `yaml:"engine"`
	Files  []FileConfig `yaml:"files"`
}

// ResolvedTracker is a tracker entry with defaults already applied.
// The values here are what the poller (and eventually backfill-and-poll-config)
// should use.
//
// TODO (backfill-and-poll-config): wire PollIntervalSeconds and BackfillDays
// into the poller's runtime cadence and git-history window.
type ResolvedTracker struct {
	Repo                string
	FacetRegex          string
	Engine              string
	Files               []FileConfig
	PollIntervalSeconds int
	BackfillDays        int
}

// Observability holds telemetry configuration for the OTel SDK. It is
// optional: an absent section (every ConfigMap written before this slice)
// parses to a zero-value Observability, and OTLPEndpoint == "" is Init's
// safe-degrade input (no backend assumed to exist). The standard
// OTEL_EXPORTER_OTLP_ENDPOINT environment variable, when set, always takes
// precedence over this value — see telemetry.ResolveOTLPEndpoint.
type Observability struct {
	// OTLPEndpoint is the OTLP endpoint ("host:port", optionally with a
	// scheme) the OTel SDK exports traces/metrics to.
	OTLPEndpoint string `yaml:"otlp_endpoint"`
}

// Config is the fully-parsed, fully-validated snapshot that consumers receive
// from Watcher.Current().
type Config struct {
	// Defaults are the global defaults as specified in the YAML.
	Defaults Defaults
	// TrackerConfigs are the per-tracker resolved configs (defaults applied).
	// Indexed parallel to the raw trackers list: TrackerConfigs[i] corresponds
	// to the i-th tracker in the YAML.
	TrackerConfigs []ResolvedTracker
	// Trackers is the flattened []domain.Tracker for the poller —
	// one entry per (repo × file-glob × field).
	Trackers []domain.Tracker
	// Observability carries telemetry config (currently just the OTLP
	// endpoint). Zero value is a valid, safe-degrade configuration.
	Observability Observability
}

// clone returns a deep copy of the Config: every slice (including the nested
// Files/Fields under each ResolvedTracker) is freshly allocated, so a caller
// that mutates the returned value cannot corrupt the live snapshot shared with
// other goroutines. Watcher.Current() hands out a clone for this reason.
func (c *Config) clone() *Config {
	if c == nil {
		return nil
	}
	cp := &Config{Defaults: c.Defaults, Observability: c.Observability}

	if c.Trackers != nil {
		cp.Trackers = make([]domain.Tracker, len(c.Trackers))
		copy(cp.Trackers, c.Trackers) // domain.Tracker has only value fields
	}

	if c.TrackerConfigs != nil {
		cp.TrackerConfigs = make([]ResolvedTracker, len(c.TrackerConfigs))
		for i, rt := range c.TrackerConfigs {
			rtCopy := rt // copies value fields (Repo, FacetRegex, ints)
			if rt.Files != nil {
				rtCopy.Files = make([]FileConfig, len(rt.Files))
				for j, f := range rt.Files {
					fCopy := f
					if f.Fields != nil {
						fCopy.Fields = make([]FieldConfig, len(f.Fields))
						copy(fCopy.Fields, f.Fields) // FieldConfig is all value fields
					}
					rtCopy.Files[j] = fCopy
				}
			}
			cp.TrackerConfigs[i] = rtCopy
		}
	}

	return cp
}
