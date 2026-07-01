// Package web (this file): the timeline page. It replaces the former
// reverse-chronological HTML feed at GET / with a server-rendered shell that
// loads a single embedded, vendored JavaScript file (via go:embed) to render
// a zoomable, single-track timeline of Changeset flags. The page itself does
// not query the store — all data comes from the browser fetching the
// existing GET /api/changesets JSON endpoint. Query, grouping,
// classification, and filter logic all stay server-side (in
// store/changeset/filter and api_changesets.go); this file and timeline.js
// only render JSON and handle marker/zoom/pan/click/clustering.
package web

import (
	"html/template"
	"log"
	"net/http"
)

// TimelineHandler serves the timeline shell page at GET /.
type TimelineHandler struct {
	tmpl *template.Template
}

// NewTimelineHandler creates a TimelineHandler and pre-parses the shell
// template. Panics if the embedded template is invalid (programming error).
func NewTimelineHandler() *TimelineHandler {
	tmpl := template.Must(template.New("timeline").Parse(timelineTemplate))
	return &TimelineHandler{tmpl: tmpl}
}

// ServeHTTP satisfies http.Handler. It renders the static shell — no store
// query is needed here; the embedded script fetches /api/changesets itself.
func (h *TimelineHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := h.tmpl.Execute(w, nil); err != nil {
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
  </style>
</head>
<body>
  <h1>Change Tracking Dashboard</h1>
  <p class="subtitle">Timeline of Changesets across config repositories.</p>
  <div id="timeline-root"></div>
  <script src="/static/timeline.js"></script>
</body>
</html>`
