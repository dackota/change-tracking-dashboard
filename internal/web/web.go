// Package web implements the HTTP feed handler: it queries the store for the
// reverse-chronological Change feed and renders it as HTML using html/template.
// The feed is server-rendered HTML with no third-party scripts; facet-based
// filtering is supported via plain GET form parameters (no JavaScript required).
package web

import (
	"html/template"
	"log"
	"net/http"
	"sort"
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

// facetOption is a single value within a facet select control.
type facetOption struct {
	Value    string
	Selected bool
}

// facetControl is one rendered select control for a facet name.
type facetControl struct {
	Name    string // e.g. "env"
	Options []facetOption
}

// feedData is the template context: the filtered Change list, the dynamic
// filter controls, and the active filter map (for constructing clear links).
type feedData struct {
	Changes        []domain.Change
	FacetControls  []facetControl
	ActiveFilters  map[string]string
}

// ServeHTTP satisfies http.Handler. It reads facet query params, queries the
// filtered feed, assembles the filter controls, and renders the page.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())

	// Fetch the set of known facet names first. URL query-param keys are
	// whitelisted against this set before reaching the SQL builder, so an
	// arbitrary param name cannot inject text into a json_extract path expression.
	// Facet values are still passed as ? parameters.
	facetOpts, err := h.st.FacetOptions()
	if err != nil {
		log.Printf("web: facet options: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Parse and sanitize facet filters: only accept keys that are known facet
	// names (from the stored data). Unknown param names are silently ignored.
	filters := parseFacetFilters(r, facetOpts)

	changes, err := h.st.QueryFilteredFeed(maxFeedItems, filters)
	if err != nil {
		// Log the detail server-side; return a generic message so internal
		// details (e.g. SQLite filesystem paths in the error) don't leak to
		// the client.
		log.Printf("web: query feed: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	controls := buildFacetControls(facetOpts, filters)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := feedData{
		Changes:       changes,
		FacetControls: controls,
		ActiveFilters: filters,
	}

	if err := h.tmpl.Execute(w, data); err != nil {
		// The response may already be partly written, so we can't change the
		// status code here — just record the failure so it's observable.
		log.Printf("web: render template: %v", err)
	}
}

// parseFacetFilters reads URL query parameters and returns a facet filter map.
// Only keys that appear in knownFacets (the set of facet names from stored data)
// are included — unknown params are silently ignored. This whitelist prevents
// arbitrary URL param names from reaching the json_extract SQL path builder in
// QueryFilteredFeed. Values are not whitelisted here; they are passed as SQL
// ? parameters in QueryFilteredFeed and rendered through html/template
// auto-escaping in the template.
func parseFacetFilters(r *http.Request, knownFacets map[string][]string) map[string]string {
	q := r.URL.Query()
	if len(q) == 0 || len(knownFacets) == 0 {
		return nil
	}
	filters := make(map[string]string, len(q))
	for k, vals := range q {
		if _, known := knownFacets[k]; !known {
			continue // not a recognised facet name — ignore
		}
		if len(vals) > 0 && vals[0] != "" {
			filters[k] = vals[0]
		}
	}
	if len(filters) == 0 {
		return nil
	}
	return filters
}

// buildFacetControls assembles a sorted slice of facetControl values from the
// observed facet options, marking which values are currently selected.
func buildFacetControls(opts map[string][]string, active map[string]string) []facetControl {
	if len(opts) == 0 {
		return nil
	}

	// Sort control names so the order is deterministic across requests.
	names := make([]string, 0, len(opts))
	for k := range opts {
		names = append(names, k)
	}
	sort.Strings(names)

	controls := make([]facetControl, 0, len(names))
	for _, name := range names {
		vals := opts[name]
		selectedVal := active[name]

		options := make([]facetOption, 0, len(vals))
		for _, v := range vals {
			options = append(options, facetOption{
				Value:    v,
				Selected: v == selectedVal,
			})
		}
		controls = append(controls, facetControl{
			Name:    name,
			Options: options,
		})
	}
	return controls
}

// setSecurityHeaders applies a conservative set of response security headers to
// every response (including error responses).
func setSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
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
  <style>
    body { font-family: system-ui, sans-serif; margin: 0; padding: 1rem 2rem; background: #f8f9fa; color: #212529; }
    h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
    .subtitle { color: #6c757d; margin-bottom: 1rem; font-size: 0.9rem; }
    .filter-bar { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: flex-end; margin-bottom: 1.25rem;
                  background: #fff; border: 1px solid #dee2e6; border-radius: 6px; padding: 0.75rem 1rem; }
    .filter-group { display: flex; flex-direction: column; gap: 0.2rem; }
    .filter-group label { font-size: 0.75rem; font-weight: 600; color: #495057; text-transform: uppercase; letter-spacing: 0.04em; }
    .filter-group select { font-size: 0.85rem; padding: 0.25rem 0.5rem; border: 1px solid #ced4da; border-radius: 4px; background: #fff; }
    .filter-actions { display: flex; gap: 0.4rem; align-items: flex-end; }
    .btn { font-size: 0.85rem; padding: 0.28rem 0.75rem; border-radius: 4px; cursor: pointer;
           border: 1px solid #ced4da; background: #fff; color: #212529; text-decoration: none; line-height: 1.6; }
    .btn-primary { background: #0d6efd; border-color: #0d6efd; color: #fff; }
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

  {{if .FacetControls}}
  <form method="GET" action="/" class="filter-bar">
    {{range .FacetControls}}
    <div class="filter-group">
      <label for="facet-{{.Name}}">{{.Name}}</label>
      <select id="facet-{{.Name}}" name="{{.Name}}">
        <option value="">All</option>
        {{range .Options}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Value}}</option>{{end}}
      </select>
    </div>
    {{end}}
    <div class="filter-actions">
      <button type="submit" class="btn btn-primary">Filter</button>
      <a href="/" class="btn">Clear</a>
    </div>
  </form>
  {{end}}

  {{if .Changes}}
  <ul class="feed">
    {{range .Changes}}
    <li class="feed-item">
      <div class="feed-header">
        <span class="field-name">{{.Field}}{{if .Key}} › {{derefStr .Key}}{{end}}</span>
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
    <p>No changes recorded yet{{if .ActiveFilters}} matching the current filters{{end}}. {{if not .ActiveFilters}}Run the poller to start tracking.{{end}}</p>
  </div>
  {{end}}
</body>
</html>`
