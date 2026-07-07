// Package web (this file): the timeline page. It renders an observability
// shell — a persistent left sidebar, a header, and a row of headline KPI
// tiles — around a loads-a-single-embedded-JavaScript-file (via go:embed)
// zoomable, single-track timeline of Changeset flags, plus a set of
// tri-state facet controls and a "Changes before T" panel. The shell chrome
// (sidebar/header/KPI tiles) is computed and rendered server-side from two
// store reads: the known facet set (FacetOptions) and a bounded slice of
// recent Changeset history fed through the pure dashboardstats package.
// Neither read changes query/grouping/classification/filter semantics, which
// all stay server-side in store/changeset/filter and api_changesets.go; the
// feed's own Changeset data still comes from the browser fetching the
// existing GET /api/changesets JSON endpoint. The template markup itself
// lives in timeline_template.go; this file and timeline.js render the known
// facet set plus computed KPI values and handle marker/zoom/pan/click/
// clustering/filter-cycling.
package web

import (
	"html/template"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/dashboardstats"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/filter"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// TimelineHandler serves the timeline shell page at GET /.
type TimelineHandler struct {
	tmpl       *template.Template
	st         *store.Store
	pollStatus PollHealthSnapshot
}

// NewTimelineHandler creates a TimelineHandler backed by st (used to fetch
// the known facet set for rendering tri-state controls) and pollStatus (used
// to render the shared header's aggregate poll-status chip, R11), and
// pre-parses the shell template. Panics if the embedded template is invalid
// (programming error).
func NewTimelineHandler(st *store.Store, pollStatus PollHealthSnapshot) *TimelineHandler {
	tmpl := template.Must(template.New("timeline").Parse(shellTemplates + timelineTemplate))
	return &TimelineHandler{tmpl: tmpl, st: st, pollStatus: pollStatus}
}

// timelineHeaderActions is the timeline page's header action button (Reset
// zoom), rendered inside the shared header shell (R2, R6). It is a
// compile-time constant, never built from request/stored data, so it is
// safe to carry as trusted template.HTML.
const timelineHeaderActions template.HTML = `<button type="button" id="header-reset-zoom" class="btn">Reset zoom</button>`

// timelineTitle and timelineSubtitle are the fixed title/subtitle rendered
// in the shared header shell for this page (R6).
const (
	timelineTitle    = "Timeline"
	timelineSubtitle = "Change activity across tracked repositories."
)

// timelineViewData is the data passed to the shell template: the shared
// shell (sidebar nav + header, R6), the headline KPI tiles, and the known
// facet set (rendered as one control per facet/value pair).
type timelineViewData struct {
	shellData
	KPI           kpiView
	FacetControls []facetControlView
}

// kpiView is the headline KPI tile row rendered from dashboardstats.Metrics
// (R3-R7): total Changes (and the Changeset count that produced them),
// distinct repository count, Chart-kind vs. value-kind Change counts, and
// the most recent Change's relative + absolute timestamp.
type kpiView struct {
	Changesets         int
	Changes            int
	Repositories       int
	ChartChanges       int
	ValueChanges       int    // Changes - ChartChanges
	LastChangeRelative string // e.g. "2 hours ago"; noChangesLabel when there is no data
	LastChangeAbsolute string // e.g. "Jul 5, 16:26"; "" when there is no data
}

// buildKPIView maps a dashboardstats.Metrics into its view representation.
// now is threaded through explicitly (rather than calling time.Now() here)
// so the relative-phrase computation stays testable against a fixed clock.
func buildKPIView(m dashboardstats.Metrics, now time.Time) kpiView {
	return kpiView{
		Changesets:         m.Changesets,
		Changes:            m.Changes,
		Repositories:       m.Repositories,
		ChartChanges:       m.ChartChanges,
		ValueChanges:       m.Changes - m.ChartChanges,
		LastChangeRelative: humanizeRelative(m.LastChangeAt, now),
		LastChangeAbsolute: formatAbsolute(m.LastChangeAt),
	}
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
// tri-state controls and a bounded slice of recent Changeset history to
// compute the headline KPI tiles; all Changeset feed data itself comes from
// the browser fetching /api/changesets.
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

	now := time.Now()
	data := timelineViewData{
		shellData:     buildShell(r.URL.Path, timelineTitle, timelineSubtitle, timelineHeaderActions, statusChip(h.pollStatus.Snapshot(), now)),
		KPI:           buildKPIView(h.loadMetrics(), now),
		FacetControls: buildFacetControls(opts),
	}
	if err := h.tmpl.Execute(w, data); err != nil {
		// The response may already be partly written, so we can't change the
		// status code here — just record the failure so it's observable.
		log.Printf("web: render timeline template: %v", err)
	}
}

// kpiHistoryCap bounds the recent Changeset history read for KPI
// aggregation — the store's own hard cap on a single QueryChangesets call.
// Aggregation itself stays in the pure dashboardstats package; this bounded
// read is the one new I/O edge this slice adds (KPI metrics are global over
// retained history in v1, not reactive to the active facet filter).
const kpiHistoryCap = store.MaxChangesetPageSize

// loadMetrics performs the bounded KPI store read and aggregates it via
// dashboardstats.Compute. On a store failure it logs the detail server-side
// and returns the zero Metrics (Changesets=0, Changes=0, Repositories=0,
// ChartChanges=0, zero LastChangeAt) rather than failing the page — mirroring
// how the FacetOptions read degrades above (R21).
func (h *TimelineHandler) loadMetrics() dashboardstats.Metrics {
	page, err := h.st.QueryChangesets(time.Now(), filter.FilterSpec{}, "", kpiHistoryCap)
	if err != nil {
		log.Printf("web: kpi changesets: %v", err)
		return dashboardstats.Metrics{}
	}
	return dashboardstats.Compute(page.Changesets)
}
