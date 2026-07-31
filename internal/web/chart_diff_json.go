// Package web (this file): the JSON wire shape for a chart-diff Outcome —
// the representation GET /api/changesets/detail/chart-diff serves when the
// caller negotiates application/json (see negotiate.go). It is a projection
// of chartdiff.Outcome onto the wire, deliberately separate from the Go type
// so the published contract can't drift by accident when the internal type
// changes.
package web

import (
	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
)

// chartDiffJSON is a chart-diff Outcome on the wire.
//
// Kind is the classification vocabulary verbatim (ok, no-prior-version,
// unavailable, could-not-render, exceeded-limits), so a client can tell "no
// changes" apart from "we could not compute this" without parsing prose.
//
// Diff is a pointer and omitted entirely for every non-ok Kind. That mirrors
// the deliberate existing decision that an Outcome never carries internal
// error detail: a non-ok response is the Kind and nothing else — no error
// strings, no Helm output, no git internals. Omitting rather than zeroing
// also keeps a genuine empty-but-successful diff distinguishable from a
// failure, which a zero-valued summary would not be.
type chartDiffJSON struct {
	Kind string           `json:"kind"`
	Diff *unifiedDiffJSON `json:"diff,omitempty"`
}

// unifiedDiffJSON is a manifestdiff.Result on the wire: the unified diff
// text, its true summary counts, and the truncation flag.
//
// Truncated is carried so a client can never present a size-ceiling-truncated
// diff as if it were complete. The summary counts are the TRUE totals,
// computed before truncation, so they stay an honest blast-radius indicator
// even when Unified was cut short.
type unifiedDiffJSON struct {
	Unified   string          `json:"unified"`
	Truncated bool            `json:"truncated"`
	Summary   lineSummaryJSON `json:"summary"`
}

// lineSummaryJSON is a manifestdiff.Summary on the wire.
type lineSummaryJSON struct {
	ManifestsChanged int `json:"manifestsChanged"`
	LinesAdded       int `json:"linesAdded"`
	LinesRemoved     int `json:"linesRemoved"`
}

// toChartDiffJSON projects a chartdiff.Outcome onto its wire shape.
func toChartDiffJSON(o chartdiff.Outcome) chartDiffJSON {
	body := chartDiffJSON{Kind: string(o.Kind)}
	if o.Kind == chartdiff.OK {
		body.Diff = toUnifiedDiffJSON(o.Diff)
	}
	return body
}

// toUnifiedDiffJSON projects a manifestdiff.Result onto its wire shape.
func toUnifiedDiffJSON(r manifestdiff.Result) *unifiedDiffJSON {
	return &unifiedDiffJSON{
		Unified:   r.Unified,
		Truncated: r.Truncated,
		Summary: lineSummaryJSON{
			ManifestsChanged: r.Summary.ManifestsChanged,
			LinesAdded:       r.Summary.LinesAdded,
			LinesRemoved:     r.Summary.LinesRemoved,
		},
	}
}
