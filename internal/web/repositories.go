// Package web (this file): the GET /repositories page (R3). It renders one
// row per tracked repository — a repository is identified by the Repo a
// Change was recorded against — listing its total Change count, its
// chart-kind Change count, and when its most recent Change was committed.
// The per-repository aggregates themselves come from store.RepositoryStats
// (R4); this file only maps that store result into a view model and renders
// it from the same shared shell (sidebar + header) every page handler uses
// (R6).
package web

import (
	"context"
	"html/template"
	"net/http"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// repositoriesTitle and repositoriesSubtitle are the fixed title/subtitle
// rendered in the shared header shell for this page (R6).
const (
	repositoriesTitle    = "Repositories"
	repositoriesSubtitle = "Tracked repositories, aggregated from recorded Changes."
)

// RepositoriesHandler serves the Repositories page at GET /repositories (R3).
type RepositoriesHandler struct {
	tmpl       *template.Template
	st         *store.Store
	pollStatus PollHealthSnapshot
}

// NewRepositoriesHandler creates a RepositoriesHandler backed by st and
// pollStatus (the poll-health registry, used for the shared header's chip,
// R11), and pre-parses the page template. Panics if the embedded template is
// invalid (a programming error, not a runtime condition).
func NewRepositoriesHandler(st *store.Store, pollStatus PollHealthSnapshot) *RepositoriesHandler {
	tmpl := template.Must(template.New("repositories").Parse(shellTemplates + repositoriesTemplate))
	return &RepositoriesHandler{tmpl: tmpl, st: st, pollStatus: pollStatus}
}

// repositoriesViewData is the data passed to the Repositories page template:
// the shared shell (R6) plus the per-repository rows (R3).
type repositoriesViewData struct {
	shellData
	Repositories []repositoryView
}

// repositoryView is one tracked repository rendered as a row (R3): its Change
// count, its chart-kind Change count, and its most recent Change's time —
// rendered as both a relative phrase and an absolute timestamp, mirroring
// the timeline KPI tile's existing "last change" pairing.
type repositoryView struct {
	Repo               string
	ChangeCount        int
	ChartChanges       int
	LastChangeRelative string
	LastChangeAbsolute string
}

// buildRepositoriesView maps a store.RepositoryStats slice into its view
// representation (R3). now is threaded through explicitly (rather than
// calling time.Now() here) so the relative-phrase computation stays testable
// against a fixed clock. A nil stats slice — the shape a degraded/failed
// store read surfaces as at this seam — or an empty one yields an empty,
// non-nil slice (R7) rather than nil, so the template's empty-state branch is
// driven by length alone. Row order is preserved as given: store.RepositoryStats
// already returns a deterministic order (R4), so this function does not
// re-sort.
func buildRepositoriesView(stats []store.RepositoryStats, now time.Time) []repositoryView {
	views := make([]repositoryView, 0, len(stats))
	for _, rs := range stats {
		views = append(views, repositoryView{
			Repo:               rs.Repo,
			ChangeCount:        rs.ChangeCount,
			ChartChanges:       rs.ChartChanges,
			LastChangeRelative: humanizeRelative(rs.LastChangeAt, now),
			LastChangeAbsolute: formatAbsolute(rs.LastChangeAt),
		})
	}
	return views
}

// ServeHTTP satisfies http.Handler.
func (h *RepositoriesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	logger := telemetry.LoggerFromContext(r.Context())

	var stats []store.RepositoryStats
	if err := telemetry.WithSpan(r.Context(), tracer, "store.repository_stats", func(context.Context) error {
		var err error
		stats, err = h.st.RepositoryStats()
		return err
	}); err != nil {
		// Log the detail server-side; render the shell anyway with an empty
		// repository list rather than failing the whole page (R7).
		logger.Error("web: repository stats", "error", err)
		stats = nil
	}

	now := time.Now()
	data := repositoriesViewData{
		shellData:    buildShell(r.URL.Path, repositoriesTitle, repositoriesSubtitle, "", statusChip(h.pollStatus.Snapshot(), h.pollStatus.ExtractFailureCounts(), now)),
		Repositories: buildRepositoriesView(stats, now),
	}
	if err := h.tmpl.Execute(w, data); err != nil {
		// The response may already be partly written, so we can't change the
		// status code here — just record the failure so it's observable.
		logger.Error("web: render repositories template", "error", err)
	}
}
