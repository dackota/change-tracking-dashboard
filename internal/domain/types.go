// Package domain defines the core types shared across all modules of the
// change-tracking-dashboard. These types represent the domain vocabulary
// established in CONTEXT.md.
package domain

import "time"

// ChangeType describes how a tracked field changed between two commits.
type ChangeType string

const (
	ChangeTypeAdded    ChangeType = "added"
	ChangeTypeRemoved  ChangeType = "removed"
	ChangeTypeModified ChangeType = "modified"
)

// Change is a detected delta in a tracked field between two consecutive commits.
// It carries the old→new values along with commit metadata for ordering and display.
type Change struct {
	Repo        string
	FilePath    string
	Field       string  // e.g. "aidp-version"
	Key         *string // nil for scalar fields; non-nil for keyed map entries
	ChangeType  ChangeType
	OldValue    *string           // nil when changeType is "added"
	NewValue    *string           // nil when changeType is "removed"
	Facets      map[string]string // e.g. {tenant: tenant-zero, env: dev, region: us-west-2}
	CommitSha   string
	Author      string
	CommittedAt time.Time // feed ordering key; newest first
}

// TrackedField is the result an Extractor yields for a single watched value.
// Present=false means the path/key was not found in the file (not an error).
//
// Two modes:
//   - Scalar: Map is nil, Value holds the stringified result.
//   - Keyed:  Map is non-nil, Value is empty. Each entry is a key→stringified-value pair.
//
// The Poller dispatches to DiffScalar or DiffKeyed based on whether Map is nil.
type TrackedField struct {
	Value   string
	Present bool
	Map     map[string]string // non-nil only for keyed (map) extraction results
}

// IsKeyed reports whether this TrackedField is a keyed map result (as opposed
// to a scalar). A keyed field routes to DiffKeyed; a scalar to DiffScalar.
func (f TrackedField) IsKeyed() bool {
	return f.Map != nil
}

// Tracker defines what to watch in one repo: a (repo, file-glob, field-name,
// extractor-expression, facet-pattern) configuration entry.
// The glob fans the tracker across many files.
type Tracker struct {
	Repo          string
	FileGlob      string
	Field         string // human-readable field name stored on Change
	ExtractorExpr string // gojq expression
	FacetPattern  string // regex with named capture groups for facet extraction

	// Engine selects which FieldExtractor implementation evaluates
	// ExtractorExpr. Empty defaults to the jq engine (today's only
	// implementation, unchanged behavior). See extractor.Select.
	Engine string

	// PollIntervalSeconds is how often this tracker is polled. Zero is not
	// valid in production but is accepted for testing/scheduling-logic purposes.
	PollIntervalSeconds int
	// BackfillDays is the number of days of git history to walk on the first
	// run (HWM empty). Zero means no history (only commits from now onward).
	BackfillDays int
}

// CommitSnapshot is the state of a single file at a particular commit.
type CommitSnapshot struct {
	CommitSha   string
	Author      string
	CommittedAt time.Time
	FilePath    string
	Content     []byte // raw file bytes at this commit; nil if file was deleted
}
