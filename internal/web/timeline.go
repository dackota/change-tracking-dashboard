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
    body { font-family: system-ui, sans-serif; margin: 0; padding: 1rem 2rem; background: #f8f9fa; color: #212529; }
    h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
    .subtitle { color: #6c757d; margin-bottom: 1rem; font-size: 0.9rem; }
    .timeline-controls { display: flex; align-items: center; gap: 1rem; margin-bottom: 0.75rem; }
    .timeline-controls label { font-size: 0.85rem; font-weight: 600; color: #495057; display: flex; align-items: center; gap: 0.4rem; }
    .timeline-controls input[type="datetime-local"] { font-size: 0.85rem; padding: 0.25rem 0.5rem; border: 1px solid #ced4da; border-radius: 4px; }
    .timeline-hint { font-size: 0.8rem; color: #6c757d; font-style: italic; }
    .facet-controls { display: flex; flex-wrap: wrap; gap: 0.4rem; margin-bottom: 1rem; }
    .facet-control { font-size: 0.8rem; padding: 0.25rem 0.6rem; border: 1px solid #ced4da; border-radius: 999px; background: #fff; cursor: pointer; }
    .facet-control[data-state="include"] { background: #0d6efd; border-color: #0d6efd; color: #fff; }
    .facet-control[data-state="exclude"] { background: #dc3545; border-color: #dc3545; color: #fff; }
    .changes-before-panel { margin-top: 1.5rem; max-width: 640px; }
    .changes-before-panel h2 { font-size: 1.1rem; margin-bottom: 0.5rem; }
    #changes-before-list { list-style: none; margin: 0; padding: 0; max-height: 400px; overflow-y: auto; border: 1px solid #dee2e6; border-radius: 4px; background: #fff; }
    #changes-before-list li { padding: 0.5rem 0.75rem; border-bottom: 1px solid #f1f3f5; font-size: 0.85rem; }
    #changes-before-list li:last-child { border-bottom: none; }
    #changes-before-load-more { margin-top: 0.5rem; font-size: 0.8rem; }
  </style>
</head>
<body>
  <h1>Change Tracking Dashboard</h1>
  <p class="subtitle">Timeline of Changesets across config repositories.</p>
  <div id="facet-controls" class="facet-controls">
    {{range .FacetControls}}<button type="button" class="facet-control" data-facet="{{.Facet}}" data-value="{{.Value}}" data-state="off">{{.Facet}}: {{.Value}}</button>
    {{end}}
  </div>
  <div id="timeline-root"></div>
  <div id="changes-before-panel" class="changes-before-panel">
    <h2>Changes before T</h2>
    <ul id="changes-before-list"></ul>
    <button type="button" id="changes-before-load-more">Load more</button>
  </div>
  <script src="/static/timeline.js"></script>
</body>
</html>`
