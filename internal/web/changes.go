// Package web (this file): the GET /changes page (R2) — a full-page Changes
// view of the changeset feed, so a user can browse change history without
// the timeline track in the way. It renders from the same shared shell
// (sidebar + header) every page handler builds via buildShell (R6), around
// the same feed-table markup and first-party script the Timeline page
// already ships. All Changeset data is fetched client-side, exactly as it is
// on the Timeline page: this handler and its template hold no store
// dependency and perform no I/O, so there is nothing here that can degrade
// to a 500 (R7) — the feed's own load/empty states are handled entirely by
// the shared timeline.js rendering this page reuses unchanged.
package web

import (
	"html/template"
	"log"
	"net/http"
)

// changesTitle and changesSubtitle are the fixed title/subtitle rendered in
// the shared header shell for this page (R6).
const (
	changesTitle    = "Changes"
	changesSubtitle = "The full changeset feed, across every tracked repository."
)

// ChangesHandler serves the Changes page at GET /changes (R2).
type ChangesHandler struct {
	tmpl *template.Template
}

// NewChangesHandler creates a ChangesHandler and pre-parses the page
// template. Panics if the embedded template is invalid (a programming
// error, not a runtime condition).
func NewChangesHandler() *ChangesHandler {
	tmpl := template.Must(template.New("changes").Parse(shellTemplates + changesTemplate))
	return &ChangesHandler{tmpl: tmpl}
}

// ServeHTTP satisfies http.Handler.
func (h *ChangesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := buildShell(r.URL.Path, changesTitle, changesSubtitle, "")
	if err := h.tmpl.Execute(w, data); err != nil {
		// The response may already be partly written, so we can't change the
		// status code here — just record the failure so it's observable.
		log.Printf("web: render changes template: %v", err)
	}
}
