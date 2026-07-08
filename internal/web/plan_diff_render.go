// Package web (this file): server-rendered HTML for the Terraform
// resource-change detail slot (GET /api/changesets/detail/plan-diff). Every
// branch is produced via html/template, which auto-escapes every
// interpolated value — a rendered resource's attribute text or unified diff
// content is untrusted tenant repository content (plandiff/manifestdiff's
// own docs: neither module escapes it) and is never concatenated into raw
// HTML here. Mirrors chart_diff_render.go's shape and CSS-class convention
// (diff-add/diff-del/diff-ctx) exactly, per acceptance criterion 8.
package web

import (
	"html/template"
	"io"

	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// planDiffTemplateSource dispatches on the plan-diff Outcome's Kind to one
// of four distinct, safely-worded fragments — never an internal error
// string (Outcome carries no internal error detail). Each fragment carries
// a stable data-kind attribute so timeline.js can style/detect it, mirroring
// changeset_detail_render.go's data-kind="terraform" convention. The OK
// branch additionally lists the per-resource delta (type/name/kind, and a
// data-forces-replacement marker feeding the risk badge a later slice
// renders) before the manifestdiff-rendered unified text.
const planDiffTemplateSource = `<div class="plan-diff plan-diff-{{.Kind}}" data-kind="{{.Kind}}">
{{if eq .Kind "ok"}}<div class="plan-diff-summary">
<span class="plan-diff-added">+{{.Added}} added</span>
<span class="plan-diff-removed">-{{.Removed}} removed</span>
<span class="plan-diff-changed">~{{.Changed}} changed</span>
{{if .Replaced}}<span class="plan-diff-replaced">{{.Replaced}} force replacement</span>{{end}}
{{if .Truncated}}<div class="plan-diff-truncated-notice">Diff truncated for length; the counts above reflect the full change.</div>{{end}}
</div>
<ul class="plan-diff-resources">
{{range .Resources}}<li class="plan-diff-resource plan-diff-resource-{{.Kind}}" data-kind="{{.Kind}}" data-forces-replacement="{{.ForcesReplacement}}">
<span class="plan-diff-resource-type">{{.ResourceType}}</span>.<span class="plan-diff-resource-name">{{.ResourceName}}</span>
<span class="plan-diff-resource-badge">{{.Kind}}</span>
{{if .ForcesReplacement}}<span class="plan-diff-resource-replace-badge">forces replacement</span>{{end}}
</li>
{{end}}</ul>
<pre class="plan-diff-unified">{{.Unified}}</pre>
{{else if eq .Kind "no-prior-version"}}<p class="plan-diff-message">No prior version to diff: this is the first version of this Terraform configuration.</p>
{{else if eq .Kind "could-not-render"}}<p class="plan-diff-message">Could not compute this plan diff.</p>
{{else if eq .Kind "exceeded-limits"}}<p class="plan-diff-message">Plan diff exceeded configured resource limits.</p>
{{end}}</div>
`

// planDiffTemplate renders a planDiffViewData into the plan-diff HTML
// fragment.
var planDiffTemplate = template.Must(template.New("plan-diff").Parse(planDiffTemplateSource))

// planDiffResourceView is one resource's classified delta, ready for
// template interpolation.
type planDiffResourceView struct {
	ResourceType      string
	ResourceName      string
	Kind              string
	ForcesReplacement bool
}

// planDiffViewData is the template input built from a plandiff.Outcome.
// Kind is carried as a plain string (rather than plandiff.Kind) so the
// template's eq comparisons never depend on any type-coercion subtlety.
type planDiffViewData struct {
	Kind      string
	Added     int
	Removed   int
	Changed   int
	Replaced  int
	Resources []planDiffResourceView
	Unified   string
	Truncated bool
}

// toPlanDiffViewData converts a plandiff.Outcome into template input.
func toPlanDiffViewData(outcome plandiff.Outcome) planDiffViewData {
	resources := make([]planDiffResourceView, len(outcome.Resources))
	for i, r := range outcome.Resources {
		resources[i] = planDiffResourceView{
			ResourceType:      r.ResourceType,
			ResourceName:      r.ResourceName,
			Kind:              string(r.Kind),
			ForcesReplacement: r.ForcesReplacement,
		}
	}
	return planDiffViewData{
		Kind:      string(outcome.Kind),
		Added:     outcome.Summary.Added,
		Removed:   outcome.Summary.Removed,
		Changed:   outcome.Summary.Changed,
		Replaced:  outcome.Summary.Replaced,
		Resources: resources,
		Unified:   outcome.Diff.Unified,
		Truncated: outcome.Diff.Truncated,
	}
}

// renderPlanDiff writes the rendered HTML fragment for outcome to w.
func renderPlanDiff(w io.Writer, outcome plandiff.Outcome) error {
	return planDiffTemplate.Execute(w, toPlanDiffViewData(outcome))
}
