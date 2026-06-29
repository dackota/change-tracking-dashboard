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
	Field       string            // e.g. "aidp-version"
	Key         *string           // nil for scalar fields; non-nil for keyed map entries
	ChangeType  ChangeType
	OldValue    *string           // nil when changeType is "added"
	NewValue    *string           // nil when changeType is "removed"
	Facets      map[string]string // e.g. {tenant: tenant-zero, env: dev, region: us-west-2}
	CommitSha   string
	Author      string
	CommittedAt time.Time         // feed ordering key; newest first
}

// TrackedField is the result an Extractor yields for a single watched value.
// Present=false means the path/key was not found in the file (not an error).
type TrackedField struct {
	Value   string
	Present bool
}

// Tracker defines what to watch in one repo: a (repo, file-glob, field-name,
// extractor-expression, facet-pattern) configuration entry.
// The glob fans the tracker across many files.
type Tracker struct {
	Repo             string
	FileGlob         string
	Field            string // human-readable field name stored on Change
	ExtractorExpr    string // gojq expression
	FacetPattern     string // regex with named capture groups for facet extraction
}

// CommitSnapshot is the state of a single file at a particular commit.
type CommitSnapshot struct {
	CommitSha   string
	Author      string
	CommittedAt time.Time
	FilePath    string
	Content     []byte // raw file bytes at this commit; nil if file was deleted
}
