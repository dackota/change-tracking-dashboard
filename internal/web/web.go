// Package web implements the HTTP feed handler: it queries the store for the
// reverse-chronological Change feed and renders it as HTML using html/template.
// The feed is server-rendered HTML with no third-party scripts; interactive
// HTMX-driven filtering arrives with the facets-and-filtering task.
//
// TODO (facets-and-filtering task): add dynamic facet filter controls to the feed UI.
package web

import (
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

const maxFeedItems = 200

// contentSecurityPolicy is strict: the feed serves first-party HTML with inline
// CSS only and no scripts, so nothing third-party needs to be allowed.
const contentSecurityPolicy = "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// Handler serves the Change feed as an HTML page.
type Handler struct {
	st   *store.Store
	tmpl *template.Template
}

// NewHandler creates a Handler backed by the given store and pre-parses the
// feed template. Panics if the embedded template is invalid (programming error).
func NewHandler(st *store.Store) *Handler {
	tmpl := template.Must(template.New("feed").Funcs(templateFuncs()).Parse(feedTemplate))
	return &Handler{st: st, tmpl: tmpl}
}

// ServeHTTP satisfies http.Handler. It queries the feed and renders it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())

	changes, err := h.st.QueryFeed(maxFeedItems)
	if err != nil {
		// Log the detail server-side; return a generic message so internal
		// details (e.g. SQLite filesystem paths in the error) don't leak to
		// the client.
		log.Printf("web: query feed: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := h.tmpl.Execute(w, feedData{Changes: changes}); err != nil {
		// The response may already be partly written, so we can't change the
		// status code here — just record the failure so it's observable.
		log.Printf("web: render template: %v", err)
	}
}

// setSecurityHeaders applies a conservative set of response security headers to
// every response (including error responses).
func setSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
}

type feedData struct {
	Changes []domain.Change
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"derefStr": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
		"formatTime": func(t time.Time) string {
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
	}
}

const feedTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Change Tracking Dashboard</title>
  <!-- TODO (facets-and-filtering task): when the feed gains interactive facet
       filtering, add HTMX vendored locally (served under script-src 'self') or
       via a Subresource-Integrity-pinned CDN tag — never an unpinned CDN. -->
  <style>
    body { font-family: system-ui, sans-serif; margin: 0; padding: 1rem 2rem; background: #f8f9fa; color: #212529; }
    h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
    .subtitle { color: #6c757d; margin-bottom: 1.5rem; font-size: 0.9rem; }
    .feed { list-style: none; padding: 0; margin: 0; }
    .feed-item { background: #fff; border: 1px solid #dee2e6; border-radius: 6px; padding: 1rem; margin-bottom: 0.75rem; }
    .feed-header { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 0.5rem; }
    .field-name { font-weight: 600; font-size: 1rem; }
    .change-type { font-size: 0.8rem; padding: 0.1rem 0.4rem; border-radius: 4px; text-transform: uppercase; letter-spacing: 0.04em; }
    .change-type-modified { background: #fff3cd; color: #856404; }
    .change-type-added    { background: #d1e7dd; color: #0f5132; }
    .change-type-removed  { background: #f8d7da; color: #842029; }
    .value-change { font-family: monospace; font-size: 0.95rem; margin-bottom: 0.5rem; }
    .old-val { color: #842029; text-decoration: line-through; }
    .new-val { color: #0f5132; }
    .meta { font-size: 0.8rem; color: #6c757d; }
    .facets { margin-top: 0.4rem; }
    .facet-tag { display: inline-block; background: #e9ecef; border-radius: 3px; padding: 0.1rem 0.35rem; font-size: 0.75rem; margin-right: 0.25rem; }
    .empty-state { text-align: center; color: #6c757d; padding: 3rem; }
  </style>
</head>
<body>
  <h1>Change Tracking Dashboard</h1>
  <p class="subtitle">Reverse-chronological feed of tracked field changes across config repositories.</p>

  {{if .Changes}}
  <ul class="feed">
    {{range .Changes}}
    <li class="feed-item">
      <div class="feed-header">
        <span class="field-name">{{.Field}}</span>
        <span class="change-type change-type-{{.ChangeType}}">{{.ChangeType}}</span>
      </div>
      <div class="value-change">
        {{if .OldValue}}<span class="old-val">{{derefStr .OldValue}}</span>{{end}}
        {{if and .OldValue .NewValue}} → {{end}}
        {{if .NewValue}}<span class="new-val">{{derefStr .NewValue}}</span>{{end}}
      </div>
      <div class="meta">
        <span title="{{.CommitSha}}">{{printf "%.8s" .CommitSha}}</span>
        &nbsp;·&nbsp; {{.Author}}
        &nbsp;·&nbsp; {{formatTime .CommittedAt}}
        &nbsp;·&nbsp; <code>{{.FilePath}}</code>
      </div>
      {{if .Facets}}
      <div class="facets">
        {{range $k, $v := .Facets}}<span class="facet-tag">{{$k}}:{{$v}}</span>{{end}}
      </div>
      {{end}}
    </li>
    {{end}}
  </ul>
  {{else}}
  <div class="empty-state">
    <p>No changes recorded yet. Run the poller to start tracking.</p>
  </div>
  {{end}}
</body>
</html>`
