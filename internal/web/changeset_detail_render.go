// Package web (this file): the per-kind rendering dispatch for a Changeset's
// detail view. A value-kind Change (source file other than Chart.yaml) is
// rendered as a plain old→new value delta. A chart-kind Change (source file
// basename Chart.yaml) is rendered distinctly as a "chart change": the
// dependency version old→new, plus a clearly-labelled empty slot reserved
// for a future full helm-template diff (deferred to a later plan — this
// slice only renders the placeholder). All HTML is produced via
// html/template, which auto-escapes every interpolated value — a Change's
// old/new value is never trusted and never concatenated into raw HTML.
package web

import (
	"fmt"
	"html/template"
	"io"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
)

// changesetDetailTemplateSource is the changeset-detail template's source.
// It dispatches each Change to the chart or value partial by comparing its
// Kind against changeset.KindChart (interpolated below as a Go string via
// fmt.Sprintf on the constant, not a hand-typed literal) — the single
// source of truth for "chart" stays internal/changeset/kind.go.
var changesetDetailTemplateSource = fmt.Sprintf(`
<section class="changeset-detail" data-commit-sha="{{.CommitSha}}">
  <header class="changeset-detail-header">
    <span class="changeset-detail-repo">{{.Repo}}</span>
    <span class="changeset-detail-commit">{{.CommitSha}}</span>
    <span class="changeset-detail-author">{{.Author}}</span>
    <time class="changeset-detail-committed-at">{{.CommittedAt.Format "2006-01-02T15:04:05Z07:00"}}</time>
  </header>
  <ul class="changeset-detail-changes">
    {{range .Changes}}
      {{if eq .Kind %q}}
        {{template "chart-change" .}}
      {{else}}
        {{template "value-change" .}}
      {{end}}
    {{end}}
  </ul>
</section>
`, changeset.KindChart)

// changesetDetailTemplate renders one Changeset's detail view: commit
// metadata followed by every one of its Changes, each dispatched to the
// value or chart partial by its Kind.
var changesetDetailTemplate = template.Must(template.New("changeset-detail").Parse(changesetDetailTemplateSource))

// valueChangeTemplate renders a value-kind Change: its field/key and the
// old→new value delta directly.
var valueChangeTemplate = template.Must(changesetDetailTemplate.New("value-change").Parse(`
<li class="change change-kind-value" data-kind="value" data-field="{{.Field}}">
  <span class="change-label">Value change</span>
  <span class="change-field">{{.Field}}{{if .Key}} [{{.Key}}]{{end}}</span>
  <span class="change-old-value">{{if .OldValue}}{{.OldValue}}{{end}}</span>
  <span class="change-arrow">&rarr;</span>
  <span class="change-new-value">{{if .NewValue}}{{.NewValue}}{{end}}</span>
</li>
`))

// chartChangeTemplate renders a chart-kind Change (Chart.yaml) distinctly
// from a value change: it is explicitly labelled "chart change", shows the
// dependency version old→new (interim rendering), and reserves a
// clearly-identifiable empty slot for the future full helm-template diff —
// that diff is deferred to a separate later plan and is intentionally left
// unrendered here.
var chartChangeTemplate = template.Must(valueChangeTemplate.New("chart-change").Parse(`
<li class="change change-kind-chart" data-kind="chart" data-field="{{.Field}}">
  <span class="change-label">Chart change</span>
  <span class="change-field">{{.Field}}{{if .Key}} [{{.Key}}]{{end}}</span>
  <span class="change-dependency-version-old">{{if .OldValue}}{{.OldValue}}{{end}}</span>
  <span class="change-arrow">&rarr;</span>
  <span class="change-dependency-version-new">{{if .NewValue}}{{.NewValue}}{{end}}</span>
  <div class="change-helm-diff-slot" data-helm-diff-pending="true">
    Full helm-template diff: not yet available (planned in a future slice)
  </div>
</li>
`))

// renderChangesetDetail writes the rendered HTML detail view for cs to w.
func renderChangesetDetail(w io.Writer, cs changeset.Changeset) error {
	return changesetDetailTemplate.Execute(w, cs)
}
