package chartdiff

import "github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/manifestdiff"

// Kind classifies a Chart diff Outcome. Diff always returns exactly one Kind
// — see Engine.Diff's doc for the classification rules.
type Kind string

const (
	// OK means the diff rendered successfully; Outcome.Diff carries the
	// unified diff and summary.
	OK Kind = "ok"
	// NoPriorVersion means the change commit is a root commit (no first
	// parent), so there is no "old" side to diff against.
	NoPriorVersion Kind = "no-prior-version"
	// Unavailable means a chart dependency declared in Chart.yaml has no
	// vendored artifact — Helm would need to pull it from a registry, which
	// this package never does (ADR 0002).
	Unavailable Kind = "unavailable"
	// CouldNotRender means the chart could not be rendered for a reason
	// other than an unvendored dependency: malformed chart content, or any
	// other unclassified failure resolving/materializing/rendering the
	// chart. This is the safe generic bucket — see Engine.Diff's doc.
	CouldNotRender Kind = "could-not-render"
	// ExceededLimits means the render hit the configured per-render timeout,
	// or materialization exceeded a configured resource ceiling.
	ExceededLimits Kind = "exceeded-limits"
)

// Outcome is the result of a Chart diff computation. Exactly one Kind is
// ever set. Outcome carries no internal error detail — only Kind and, for
// Kind == OK, the manifestdiff.Result — so a caller (the web slice) can
// render a safe, classification-driven message without any risk of leaking
// Helm/git internals. The underlying cause of a non-OK Outcome is logged
// server-side by the Engine, never attached here.
type Outcome struct {
	// Kind classifies this Outcome.
	Kind Kind
	// Diff is the unified diff + summary, populated only when Kind == OK.
	Diff manifestdiff.Result
}
