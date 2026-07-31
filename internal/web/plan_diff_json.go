// Package web (this file): the JSON wire shape for a plan-diff Outcome — the
// representation GET /api/changesets/detail/plan-diff serves when the caller
// negotiates application/json (see negotiate.go). Mirrors chart_diff_json.go's
// role for the sibling endpoint, and reuses its unifiedDiffJSON for the
// line-level half so the two endpoints describe a unified diff identically on
// the wire.
package web

import "github.com/dackota/change-tracking-dashboard/internal/plandiff"

// planDiffJSON is a plan-diff Outcome on the wire.
//
// Kind is the classification vocabulary verbatim (ok, no-prior-version,
// could-not-render, exceeded-limits). There is no `unavailable` here — a
// Terraform resource block is always statically resolvable from the
// materialized subtree, so this endpoint has no registry-pull case to decline.
//
// Diff, Summary, and Resources are all omitted entirely for a non-ok Kind, for
// the same reason as chartDiffJSON.Diff: a non-ok response is the Kind and
// nothing else — no HCL-parser internals, no git internals, no error strings.
type planDiffJSON struct {
	Kind      string               `json:"kind"`
	Diff      *unifiedDiffJSON     `json:"diff,omitempty"`
	Summary   *resourceSummaryJSON `json:"summary,omitempty"`
	Resources []resourceDeltaJSON  `json:"resources,omitempty"`
}

// resourceSummaryJSON is a plandiff.Summary on the wire: the aggregate
// resource-level blast radius, so a one-line summary ("2 resources force
// replacement") needs no client-side computation over Resources.
//
// Replaced is how many of Removed+Changed are flagged forcesReplacement — a
// subset of those two, never counted separately from them.
type resourceSummaryJSON struct {
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Changed  int `json:"changed"`
	Replaced int `json:"replaced"`
}

// resourceDeltaJSON is one plandiff.ResourceDelta on the wire, identified by
// its (Type, Name) HCL address.
//
// ForcesReplacement is the destructive-change flag a reviewer most needs to
// see before merging: true for a removal (always destructive) or a change that
// touched a force-replacement attribute, and always false for an addition.
type resourceDeltaJSON struct {
	Type              string `json:"type"`
	Name              string `json:"name"`
	Kind              string `json:"kind"`
	ForcesReplacement bool   `json:"forcesReplacement"`
}

// toPlanDiffJSON projects a plandiff.Outcome onto its wire shape.
//
// Resources is emitted in the engine's existing deterministic (ResourceType,
// ResourceName) sorted order — this function preserves that order rather than
// imposing one, so the wire order is stable across requests for free and
// cannot drift from the order the HTML rendering shows.
func toPlanDiffJSON(o plandiff.Outcome) planDiffJSON {
	body := planDiffJSON{Kind: string(o.Kind)}
	if o.Kind != plandiff.OK {
		return body
	}

	body.Diff = toUnifiedDiffJSON(o.Diff)
	body.Summary = &resourceSummaryJSON{
		Added:    o.Summary.Added,
		Removed:  o.Summary.Removed,
		Changed:  o.Summary.Changed,
		Replaced: o.Summary.Replaced,
	}
	body.Resources = make([]resourceDeltaJSON, 0, len(o.Resources))
	for _, d := range o.Resources {
		body.Resources = append(body.Resources, resourceDeltaJSON{
			Type:              d.ResourceType,
			Name:              d.ResourceName,
			Kind:              string(d.Kind),
			ForcesReplacement: d.ForcesReplacement,
		})
	}
	return body
}
