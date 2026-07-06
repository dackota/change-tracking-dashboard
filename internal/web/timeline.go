// Package web (this file): the timeline page. It replaces the former
// reverse-chronological HTML feed at GET / with a server-rendered shell that
// loads a single embedded, vendored JavaScript file (via go:embed) to render
// a zoomable, single-track timeline of Changeset flags, plus a set of
// tri-state facet controls and a "Changes before T" panel. The page itself
// renders one control per known facet value (sourced from the store's
// FacetOptions) but does not otherwise query the store for Changeset data —
// that comes from the browser fetching the existing GET /api/changesets JSON
// endpoint. Query, grouping, classification, and filter logic all stay
// server-side (in store/changeset/filter and api_changesets.go); this file
// and timeline.js only render the known facet set and handle marker/zoom/
// pan/click/clustering/filter-cycling.
package web

import (
	"html/template"
	"log"
	"net/http"
	"sort"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// TimelineHandler serves the timeline shell page at GET /.
type TimelineHandler struct {
	tmpl *template.Template
	st   *store.Store
}

// NewTimelineHandler creates a TimelineHandler backed by st (used to fetch
// the known facet set for rendering tri-state controls) and pre-parses the
// shell template. Panics if the embedded template is invalid (programming
// error).
func NewTimelineHandler(st *store.Store) *TimelineHandler {
	tmpl := template.Must(template.New("timeline").Parse(timelineTemplate))
	return &TimelineHandler{tmpl: tmpl, st: st}
}

// timelineViewData is the data passed to the shell template: the known facet
// set, rendered as one control per facet/value pair.
type timelineViewData struct {
	FacetControls []facetControlView
}

// facetControlView is one facet name/value pair to render as a tri-state
// control. html/template auto-escapes Facet and Value wherever they appear
// in the template, so a facet value containing HTML-significant characters
// is never rendered raw.
type facetControlView struct {
	Facet string
	Value string
}

// buildFacetControls flattens a facetName -> values map (as returned by
// store.FacetOptions) into a deterministically ordered slice of controls —
// sorted by facet name, then by value — so the rendered control order never
// depends on Go's randomized map iteration.
func buildFacetControls(opts map[string][]string) []facetControlView {
	names := make([]string, 0, len(opts))
	for name := range opts {
		names = append(names, name)
	}
	sort.Strings(names)

	controls := make([]facetControlView, 0, len(opts))
	for _, name := range names {
		values := append([]string(nil), opts[name]...)
		sort.Strings(values)
		for _, v := range values {
			controls = append(controls, facetControlView{Facet: name, Value: v})
		}
	}
	return controls
}

// ServeHTTP satisfies http.Handler. It fetches the known facet set to render
// tri-state controls; all Changeset data itself comes from the browser
// fetching /api/changesets.
func (h *TimelineHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	opts, err := h.st.FacetOptions()
	if err != nil {
		// Log the detail server-side; render the shell anyway with no facet
		// controls rather than failing the whole page — a rendering
		// convenience should never take down the timeline itself.
		log.Printf("web: facet options: %v", err)
		opts = nil
	}

	data := timelineViewData{FacetControls: buildFacetControls(opts)}
	if err := h.tmpl.Execute(w, data); err != nil {
		// The response may already be partly written, so we can't change the
		// status code here — just record the failure so it's observable.
		log.Printf("web: render timeline template: %v", err)
	}
}

const timelineTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Change Tracking Dashboard</title>
  <style>
    :root {
      --ink:#212529; --muted:#6c757d; --line:#dee2e6; --line-soft:#f1f3f5;
      --blue:#0d6efd; --red:#dc3545; --surface:#fff; --bg:#f8f9fa;
      --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    }
    * { box-sizing: border-box; }
    body { font-family: system-ui, -apple-system, sans-serif; margin: 0; padding: 1.25rem 2rem 4rem; background: var(--bg); color: var(--ink); }
    h1 { font-size: 1.5rem; margin: 0 0 0.15rem; }
    .subtitle { color: var(--muted); margin: 0 0 1.25rem; font-size: 0.9rem; }

    /* Facet dropdowns */
    .facet-bar { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; margin-bottom: 1rem; }
    .facet-bar-label { font-size: 0.8rem; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.04em; }
    .facet-controls.facet-dropdowns { display: flex; flex-wrap: wrap; gap: 0.5rem; }
    /* Progressive-enhancement fallback: raw controls before JS builds dropdowns */
    .facet-control { font-size: 0.8rem; padding: 0.25rem 0.6rem; border: 1px solid #ced4da; border-radius: 999px; background: #fff; cursor: pointer; }
    .facet-control[data-state="include"] { background: var(--blue); border-color: var(--blue); color: #fff; }
    .facet-control[data-state="exclude"] { background: var(--red); border-color: var(--red); color: #fff; }
    details.facet-dd { border: 1px solid var(--line); border-radius: 8px; background: var(--surface); font-size: 0.83rem; }
    details.facet-dd > summary { list-style: none; cursor: pointer; padding: 0.35rem 0.7rem; display: flex; align-items: center; gap: 0.4rem; font-weight: 600; color: #343a40; user-select: none; }
    details.facet-dd > summary::-webkit-details-marker { display: none; }
    details.facet-dd > summary::after { content: "▾"; color: var(--muted); font-size: 0.75rem; }
    details.facet-dd[open] > summary { border-bottom: 1px solid var(--line-soft); }
    .facet-dd-name { text-transform: capitalize; }
    .facet-dd-badge { background: var(--blue); color: #fff; font-size: 0.68rem; font-weight: 700; border-radius: 999px; min-width: 1.1rem; text-align: center; padding: 0 0.35rem; }
    .facet-dd-body { padding: 0.4rem 0.5rem; display: flex; flex-direction: column; gap: 0.3rem; max-height: 260px; overflow-y: auto; min-width: 190px; }
    .facet-row { display: flex; align-items: center; gap: 0.4rem; }
    .facet-pill { flex: 1 1 auto; text-align: left; font-size: 0.8rem; padding: 0.2rem 0.55rem; border: 1px solid #ced4da; border-radius: 6px; background: #fff; cursor: pointer; color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .facet-pill[data-state="include"] { background: var(--blue); border-color: var(--blue); color: #fff; }
    .facet-pill[data-state="exclude"] { background: var(--red); border-color: var(--red); color: #fff; }
    .facet-pill[data-state="include"]::before { content: "✓ "; }
    .facet-pill[data-state="exclude"]::before { content: "✕ "; }
    .facet-only { font-size: 0.7rem; color: var(--blue); background: none; border: 1px solid transparent; border-radius: 5px; cursor: pointer; padding: 0.15rem 0.35rem; }
    .facet-only:hover { border-color: var(--blue); }
    .facet-clear { font-size: 0.78rem; font-weight: 600; padding: 0.3rem 0.7rem; border: 1px solid var(--line); background: #fff; border-radius: 6px; cursor: pointer; color: #495057; }
    .facet-clear:hover { border-color: var(--red); color: var(--red); }
    .facet-clear[hidden] { display: none; }

    /* Timeline controls + track */
    #timeline-root { background: var(--surface); border: 1px solid var(--line); border-radius: 10px; padding: 0.75rem 0.9rem; }
    .timeline-controls { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.6rem; flex-wrap: wrap; }
    .range-toggle { font-size: 0.8rem; font-weight: 600; padding: 0.3rem 0.7rem; border: 1px solid var(--blue); color: var(--blue); background: #fff; border-radius: 6px; cursor: pointer; }
    .range-toggle[data-active="true"] { background: var(--blue); color: #fff; }
    .range-label { font-size: 0.8rem; font-weight: 600; color: #495057; display: flex; align-items: center; gap: 0.35rem; }
    .range-label input { font-size: 0.8rem; padding: 0.2rem 0.4rem; border: 1px solid #ced4da; border-radius: 4px; }
    .range-clear { font-size: 0.78rem; padding: 0.25rem 0.6rem; border: 1px solid var(--line); background: #fff; border-radius: 6px; cursor: pointer; color: #495057; }
    .range-clear:disabled { opacity: 0.45; cursor: default; }
    .timeline-hint { font-size: 0.76rem; color: var(--muted); font-style: italic; }
    .timeline-svg { display: block; max-width: 100%; }

    /* Feed */
    .feed-panel { margin-top: 1.5rem; max-width: 820px; }
    .feed-head { display: flex; align-items: baseline; gap: 0.6rem; margin-bottom: 0.5rem; }
    .feed-head h2 { font-size: 1.1rem; margin: 0; }
    .feed-count { font-size: 0.8rem; color: var(--muted); }
    .feed-list { list-style: none; margin: 0; padding: 0; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); overflow: hidden; }
    .feed-row { display: flex; align-items: center; gap: 0.6rem; padding: 0.55rem 0.8rem; border-bottom: 1px solid var(--line-soft); font-size: 0.85rem; cursor: pointer; }
    .feed-row:last-child { border-bottom: none; }
    .feed-row:hover { background: #f8fbff; }
    .feed-dot { width: 9px; height: 9px; border-radius: 50%; flex: none; }
    .feed-time { font-variant-numeric: tabular-nums; color: #495057; white-space: nowrap; }
    .feed-repo { font-weight: 600; color: var(--ink); }
    .feed-commit { font-family: var(--mono); font-size: 0.8rem; color: var(--blue); text-decoration: none; }
    .feed-commit:hover { text-decoration: underline; }
    .feed-commit-plain { color: var(--muted); }
    .feed-author { color: var(--muted); }
    .feed-count-badge { margin-left: auto; font-size: 0.75rem; color: #495057; background: var(--line-soft); border-radius: 999px; padding: 0.1rem 0.5rem; white-space: nowrap; }
    .feed-empty { padding: 1.5rem 1rem; text-align: center; color: var(--muted); font-size: 0.9rem; border: 1px dashed var(--line); border-radius: 8px; background: var(--surface); display: flex; flex-direction: column; align-items: center; gap: 0.8rem; }
    .feed-empty[hidden] { display: none; }
    .feed-clear-btn { font-size: 0.8rem; padding: 0.3rem 0.8rem; border: 1px solid var(--blue); color: var(--blue); background: #fff; border-radius: 6px; cursor: pointer; }

    /* Detail panel */
    #timeline-detail-panel { margin-top: 1.25rem; }
    .changeset-detail { border: 1px solid var(--line); border-radius: 10px; background: var(--surface); padding: 0.9rem 1rem; margin-bottom: 1rem; }
    .changeset-detail-header { display: flex; align-items: center; gap: 0.7rem; flex-wrap: wrap; padding-bottom: 0.6rem; border-bottom: 1px solid var(--line-soft); margin-bottom: 0.6rem; }
    .changeset-detail-repo { font-weight: 700; }
    .changeset-detail-commit { font-family: var(--mono); font-size: 0.82rem; color: var(--blue); text-decoration: none; }
    .changeset-detail-commit:hover { text-decoration: underline; }
    .changeset-detail-author { color: var(--muted); font-size: 0.85rem; }
    .changeset-detail-committed-at { color: var(--muted); font-size: 0.85rem; font-variant-numeric: tabular-nums; margin-left: auto; }
    .changeset-detail-changes { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.75rem; }
    .change { display: flex; flex-wrap: wrap; align-items: center; gap: 0.4rem; font-size: 0.86rem; }
    .change-label { font-size: 0.68rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.03em; padding: 0.1rem 0.4rem; border-radius: 4px; }
    .change-kind-chart .change-label { background: #e7f1ff; color: #084298; }
    .change-kind-value .change-label { background: var(--line-soft); color: #495057; }
    .change-field { font-weight: 600; }
    .change-old-value, .change-dependency-version-old { font-family: var(--mono); color: var(--red); }
    .change-new-value, .change-dependency-version-new { font-family: var(--mono); color: #198754; }
    .change-arrow { color: var(--muted); }
    .change-helm-diff-slot { flex-basis: 100%; margin-top: 0.4rem; font-size: 0.82rem; color: var(--muted); }

    /* Chart diff summary + color-coded hunks */
    .chart-diff-summary { display: flex; gap: 0.6rem; align-items: center; font-size: 0.82rem; margin-bottom: 0.5rem; }
    .chart-diff-manifests-changed { font-weight: 600; color: var(--ink); }
    .chart-diff-lines-added { color: #198754; font-weight: 700; font-family: var(--mono); }
    .chart-diff-lines-removed { color: var(--red); font-weight: 700; font-family: var(--mono); }
    .chart-diff-truncated-notice { font-size: 0.78rem; color: #b8860b; }
    .chart-diff-message { font-size: 0.85rem; color: var(--muted); font-style: italic; }
    .diff-hunks { font-family: var(--mono); font-size: 0.78rem; line-height: 1.5; border: 1px solid var(--line); border-radius: 8px; overflow-x: auto; background: var(--surface); max-height: 460px; overflow-y: auto; }
    .diff-line { white-space: pre; padding: 0 0.7rem; }
    .diff-add { background: #e6ffed; color: #04260f; }
    .diff-del { background: #ffeef0; color: #3d0a12; }
    .diff-ctx { color: #868e96; }
    .diff-gap { background: var(--line-soft); color: #868e96; font-style: italic; text-align: center; font-family: system-ui, sans-serif; padding: 0.2rem 0.7rem; border-top: 1px solid #eceff1; border-bottom: 1px solid #eceff1; }
  </style>
</head>
<body>
  <h1>Change Tracking Dashboard</h1>
  <p class="subtitle">Timeline of Changesets across config repositories.</p>
  <div class="facet-bar">
    <span class="facet-bar-label">Filter</span>
    <div id="facet-controls" class="facet-controls">
      {{range .FacetControls}}<button type="button" class="facet-control" data-facet="{{.Facet}}" data-value="{{.Value}}" data-state="off">{{.Facet}}: {{.Value}}</button>
      {{end}}
    </div>
    <button type="button" id="facet-clear" class="facet-clear" hidden>Clear filters</button>
  </div>
  <div id="timeline-root"></div>
  <div id="feed-panel" class="feed-panel">
    <div class="feed-head">
      <h2 id="feed-title">Changes</h2>
      <span id="feed-count" class="feed-count"></span>
    </div>
    <ul id="feed-list" class="feed-list"></ul>
    <div id="feed-empty" class="feed-empty" hidden></div>
  </div>
  <script src="/static/timeline.js"></script>
</body>
</html>`
