// Package web (this file): the per-kind rendering dispatch for a Changeset's
// detail view. A value-kind Change (source file other than Chart.yaml) is
// rendered as a plain old→new value delta. A chart-kind Change (source file
// basename Chart.yaml) is rendered distinctly as a "chart change": the
// dependency version old→new, plus a helm-diff slot that timeline.js wires
// live — it shows a "Rendering diff…" state and fetches this Change's own
// Chart diff from GET /api/changesets/detail/chart-diff, using the
// data-tenant-path attribute rendered here. All HTML is produced via
// html/template, which auto-escapes every interpolated value — a Change's
// old/new value is never trusted and never concatenated into raw HTML.
package web

import (
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
)

// gitSuffixPattern matches a trailing ".git" suffix together with any
// slash(es) immediately before or after it ("/.git", ".git/", "/.git/",
// "//.git//", …) so commitURL's suffix strip below never leaves a slash that
// preceded ".git" stranded once ".git" itself is removed.
var gitSuffixPattern = regexp.MustCompile(`/*\.git/*$`)

// changesetDetailTemplateSource is the changeset-detail template's source.
// It dispatches each Change to the chart or value partial by comparing its
// Kind against changeset.KindChart (interpolated below as a Go string via
// fmt.Sprintf on the constant, not a hand-typed literal) — the single
// source of truth for "chart" stays internal/changeset/kind.go.
var changesetDetailTemplateSource = fmt.Sprintf(`
<section class="changeset-detail" data-commit-sha="{{.CommitSha}}">
  <header class="changeset-detail-header">
    <span class="changeset-detail-repo" title="{{.Repo}}">{{.RepoName}}</span>
    {{if .CommitURL}}<a class="changeset-detail-commit" href="{{.CommitURL}}" target="_blank" rel="noopener noreferrer" title="{{.CommitSha}}">{{.ShortSha}}</a>{{else}}<span class="changeset-detail-commit" title="{{.CommitSha}}">{{.ShortSha}}</span>{{end}}
    <span class="changeset-detail-author">{{.Author}}</span>
    <time class="changeset-detail-committed-at">{{.CommittedAt.Format "2006-01-02 15:04"}}</time>
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
// dependency version old→new (interim rendering), and carries a
// clearly-identifiable helm-diff slot that timeline.js wires live: the
// data-tenant-path attribute (the directory of this Change's own source
// file — see newChangesetView) is what timeline.js reads to build its GET
// /api/changesets/detail/chart-diff fetch URL for this specific slot.
var chartChangeTemplate = template.Must(valueChangeTemplate.New("chart-change").Parse(`
<li class="change change-kind-chart" data-kind="chart" data-field="{{.Field}}">
  <span class="change-label">Chart change</span>
  <span class="change-field">{{.Field}}{{if .Key}} [{{.Key}}]{{end}}</span>
  <span class="change-dependency-version-old">{{if .OldValue}}{{.OldValue}}{{end}}</span>
  <span class="change-arrow">&rarr;</span>
  <span class="change-dependency-version-new">{{if .NewValue}}{{.NewValue}}{{end}}</span>
  <div class="change-helm-diff-slot" data-helm-diff-pending="true" data-tenant-path="{{.TenantPath}}">
    Full helm-template diff: not yet available (planned in a future slice)
  </div>
</li>
`))

// changeView is a classified Change plus TenantPath: the directory of the
// Change's own FilePath (filepath.Dir), matching the PRD's "Rendering
// basis" — the tenant chart directory is the directory of the chart
// Change's source file — and how GET /api/changesets/detail/chart-diff's
// own TenantPath is documented to be derived. html/template has no
// path.Dir function of its own, so this is computed here, once, before
// Execute, rather than in the template language. Every Change carries a
// TenantPath, though only the chart-change partial renders it.
type changeView struct {
	changeset.Change
	TenantPath string
}

// changesetView is the changeset-detail template's top-level view model:
// cs's own commit metadata, with its Changes projected through changeView.
type changesetView struct {
	Repo        string
	RepoName    string // short, human-friendly repo name (basename, .git stripped)
	CommitSha   string
	ShortSha    string // first 8 chars of CommitSha for compact display
	CommitURL   string // web URL to the commit, empty for non-URL (local-path) repos
	Author      string
	CommittedAt time.Time
	Changes     []changeView
}

// newChangesetView builds the template view model for cs.
func newChangesetView(cs changeset.Changeset) changesetView {
	changes := make([]changeView, 0, len(cs.Changes))
	for _, c := range cs.Changes {
		changes = append(changes, changeView{Change: c, TenantPath: filepath.Dir(c.FilePath)})
	}
	return changesetView{
		Repo:        cs.Repo,
		RepoName:    repoShortName(cs.Repo),
		CommitSha:   cs.CommitSha,
		ShortSha:    shortSha(cs.CommitSha),
		CommitURL:   commitURL(cs.Repo, cs.CommitSha),
		Author:      cs.Author,
		CommittedAt: cs.CommittedAt,
		Changes:     changes,
	}
}

// repoShortName reduces a repo path or URL to a human-friendly name: the last
// path segment with any trailing "/" and ".git" suffix removed. Works for both
// local paths ("/repos/free-tier-oracle-cloud-k8s" → "free-tier-oracle-cloud-
// k8s") and remote URLs ("https://github.com/o/r.git" → "r"). Falls back to
// the original string if reduction would yield empty.
func repoShortName(repo string) string {
	r := strings.TrimSuffix(strings.TrimRight(repo, "/"), ".git")
	if i := strings.LastIndex(r, "/"); i >= 0 {
		r = r[i+1:]
	}
	if r == "" {
		return repo
	}
	return r
}

// commitURL derives a web URL to a commit for HTTP(S) git remotes
// ("https://github.com/o/r(.git)" → "https://github.com/o/r/commit/<sha>").
// Returns "" for local-path repos (no browsable URL) or an empty sha, so the
// template renders a plain short-sha span instead of a link.
func commitURL(repo, sha string) string {
	if sha == "" {
		return ""
	}
	if !strings.HasPrefix(repo, "http://") && !strings.HasPrefix(repo, "https://") {
		return ""
	}
	// Strip a trailing ".git" suffix (and any slash(es) around it) BEFORE
	// trimming trailing slashes. Trimming trailing slashes first (the prior,
	// buggy order) can leave a slash that preceded ".git" stranded once
	// ".git" is removed (e.g. ".../repo/.git" -> ".../repo/"), which then
	// collides with the leading "/" of "/commit/<sha>" into a double slash.
	base := strings.TrimRight(gitSuffixPattern.ReplaceAllString(repo, ""), "/")
	return base + "/commit/" + sha
}

// shortSha returns the first 8 characters of a commit sha (the whole sha if it
// is shorter), for compact display alongside the full sha in a title tooltip.
func shortSha(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// renderChangesetDetail writes the rendered HTML detail view for cs to w.
func renderChangesetDetail(w io.Writer, cs changeset.Changeset) error {
	return changesetDetailTemplate.Execute(w, newChangesetView(cs))
}
