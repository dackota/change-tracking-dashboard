// Package web (this file): server-rendered HTML for the Chart diff detail
// slot (GET /api/changesets/detail/chart-diff). Every branch is produced via
// html/template, which auto-escapes every interpolated value — a rendered
// manifest's or a unified diff's content is untrusted tenant repository
// content (chartdiff/manifestdiff's own docs: neither module escapes it) and
// is never concatenated into raw HTML here.
package web

import (
	"html/template"
	"io"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
)

// chartDiffTemplateSource dispatches on the Chart diff Outcome's Kind to one
// of five distinct, safely-worded fragments — never an internal error
// string (Outcome carries no internal error detail; see its own doc). Each
// fragment carries a stable data-kind attribute and a per-kind class so a
// later slice (timeline.js wiring, out of scope here) can style/detect it,
// mirroring changeset_detail_render.go's data-kind="chart" convention.
const chartDiffTemplateSource = `<div class="chart-diff chart-diff-{{.Kind}}" data-kind="{{.Kind}}">
{{if eq .Kind "ok"}}<div class="chart-diff-summary">
<span class="chart-diff-manifests-changed">{{.ManifestsChanged}} manifest(s) changed</span>
<span class="chart-diff-lines-added">+{{.LinesAdded}}</span>
<span class="chart-diff-lines-removed">-{{.LinesRemoved}}</span>
{{if .Truncated}}<div class="chart-diff-truncated-notice">Diff truncated for length; the counts above reflect the full change.</div>{{end}}
</div>
<pre class="chart-diff-unified">{{.Unified}}</pre>
{{else if eq .Kind "no-prior-version"}}<p class="chart-diff-message">No prior version to diff: this is the first version of this chart.</p>
{{else if eq .Kind "unavailable"}}<p class="chart-diff-message">Chart diff unavailable: this chart's dependency is not vendored in this repository, so it cannot be rendered offline. Registry-pull rendering is planned as a future capability.</p>
{{else if eq .Kind "could-not-render"}}<p class="chart-diff-message">Could not render this chart diff.</p>
{{else if eq .Kind "exceeded-limits"}}<p class="chart-diff-message">Chart diff exceeded configured render limits.</p>
{{end}}</div>
`

// chartDiffTemplate renders a chartDiffViewData into the chart-diff HTML
// fragment.
var chartDiffTemplate = template.Must(template.New("chart-diff").Parse(chartDiffTemplateSource))

// chartDiffViewData is the template input built from a chartdiff.Outcome.
// Kind is carried as a plain string (rather than chartdiff.Kind) so the
// template's eq comparisons never depend on any type-coercion subtlety.
type chartDiffViewData struct {
	Kind             string
	ManifestsChanged int
	LinesAdded       int
	LinesRemoved     int
	Unified          string
	Truncated        bool
}

// toChartDiffViewData converts a chartdiff.Outcome into template input.
func toChartDiffViewData(outcome chartdiff.Outcome) chartDiffViewData {
	return chartDiffViewData{
		Kind:             string(outcome.Kind),
		ManifestsChanged: outcome.Diff.Summary.ManifestsChanged,
		LinesAdded:       outcome.Diff.Summary.LinesAdded,
		LinesRemoved:     outcome.Diff.Summary.LinesRemoved,
		Unified:          outcome.Diff.Unified,
		Truncated:        outcome.Diff.Truncated,
	}
}

// renderChartDiff writes the rendered HTML fragment for outcome to w.
func renderChartDiff(w io.Writer, outcome chartdiff.Outcome) error {
	return chartDiffTemplate.Execute(w, toChartDiffViewData(outcome))
}
