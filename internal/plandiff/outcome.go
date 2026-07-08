package plandiff

import "github.com/dackota/change-tracking-dashboard/internal/manifestdiff"

// Kind classifies a static plan-diff Outcome. Diff always returns exactly one
// Kind — see Engine.Diff's doc for the classification rules. It deliberately
// mirrors chartdiff.Kind's vocabulary (OK/NoPriorVersion/CouldNotRender/
// ExceededLimits) minus chartdiff's Unavailable, which has no analogue here:
// a Terraform resource block is always statically resolvable from the
// materialized subtree alone — there is no external chart-dependency-style
// registry pull plandiff could ever need and decline to do.
type Kind string

const (
	// OK means the diff computed successfully; Outcome.Diff, Outcome.Summary,
	// and Outcome.Resources are all populated.
	OK Kind = "ok"
	// NoPriorVersion means the change commit is a root commit (no first
	// parent), so there is no "old" side to diff against.
	NoPriorVersion Kind = "no-prior-version"
	// CouldNotRender means the resource set could not be computed for a
	// reason other than a resource bound: malformed/unparseable HCL, or any
	// other unclassified failure resolving/materializing/parsing either
	// side. This is the safe generic bucket — see Engine.Diff's doc.
	CouldNotRender Kind = "could-not-render"
	// ExceededLimits means materialization exceeded a configured resource
	// ceiling, a materialize/parse call exceeded its configured timeout, or
	// a resource body's nested-block recursion exceeded Config.MaxBlockDepth.
	ExceededLimits Kind = "exceeded-limits"
)

// ResourceChangeKind classifies how a single resource changed between the
// old and new sides of a plan-diff.
type ResourceChangeKind string

const (
	// ResourceAdded means the resource exists only on the new side.
	ResourceAdded ResourceChangeKind = "added"
	// ResourceRemoved means the resource exists only on the old side.
	ResourceRemoved ResourceChangeKind = "removed"
	// ResourceChanged means the resource exists on both sides with a
	// different rendered body.
	ResourceChanged ResourceChangeKind = "changed"
)

// ResourceDelta is one resource's classified change between the old and new
// sides of a plan-diff, identified by its (ResourceType, ResourceName) HCL
// address (e.g. resource "oci_core_instance" "web" -> Type
// "oci_core_instance", Name "web").
type ResourceDelta struct {
	ResourceType string
	ResourceName string
	Kind         ResourceChangeKind
	// ForcesReplacement is plandiff's heuristic replacement-forcing flag
	// (acceptance criterion 2 / PRD R18): true when the resource was
	// removed entirely (Kind == ResourceRemoved — a removal is always
	// destructive), or when Kind == ResourceChanged and at least one of
	// Config.ForceReplacementAttrs changed value. Always false for Kind ==
	// ResourceAdded — a brand-new resource replaces nothing.
	ForcesReplacement bool
}

// Summary is the resource-level blast-radius overview of a plan-diff:
// acceptance criterion 1's "resources added, removed, and attribute-changed"
// counts, plus criterion 2's replacement-forcing count.
type Summary struct {
	Added   int
	Removed int
	Changed int
	// Replaced is how many of the Removed+Changed resources are flagged
	// ForcesReplacement — a subset of (Removed + Changed), never counted
	// separately from them.
	Replaced int
}

// Outcome is the result of a plan-diff computation. Exactly one Kind is ever
// set. Outcome carries no internal error detail — only Kind and, for Kind ==
// OK, the resource-level Summary/Resources and the manifestdiff-rendered
// unified text — so a caller (the web slice) can render a safe,
// classification-driven view without any risk of leaking git/HCL-parser
// internals. The underlying cause of a non-OK Outcome is logged server-side
// by the Engine, never attached here.
type Outcome struct {
	// Kind classifies this Outcome.
	Kind Kind
	// Diff is the manifestdiff-rendered unified text + line-count summary,
	// populated only when Kind == OK.
	Diff manifestdiff.Result
	// Summary is the resource-level added/removed/changed/replaced counts,
	// populated only when Kind == OK.
	Summary Summary
	// Resources is the per-resource classified delta, populated only when
	// Kind == OK, in deterministic (ResourceType, ResourceName) sorted
	// order.
	Resources []ResourceDelta
}
