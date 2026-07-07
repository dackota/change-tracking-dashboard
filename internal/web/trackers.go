// Package web (this file): the GET /trackers page (R5). It renders the
// configured trackers — a tracker is a (repo, file-glob, extractor) triple
// per the domain glossary — read from the live, hot-reloaded config
// snapshot: for each one, its repo, the file globs it watches, the tracked
// fields (one watched value per extractor) those globs yield, how often it
// polls, and the backfill window walked on first run. Renders from the same
// shared shell (sidebar + header) every page handler builds via buildShell
// (R6).
package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
)

// trackersTitle and trackersSubtitle are the fixed title/subtitle rendered
// in the shared header shell for this page (R6).
const (
	trackersTitle    = "Trackers"
	trackersSubtitle = "Configured trackers, read from the live config snapshot."
)

// ConfigSnapshot is the seam TrackersHandler depends on to read the live
// tracker config — satisfied directly by *config.Watcher. Defined here, at
// the point of use, per this project's small-interfaces convention.
type ConfigSnapshot interface {
	Current() *config.Config
}

// TrackersHandler serves the Trackers page at GET /trackers (R5).
type TrackersHandler struct {
	tmpl *template.Template
	cfg  ConfigSnapshot
}

// NewTrackersHandler creates a TrackersHandler backed by cfg and pre-parses
// the page template. Panics if the embedded template is invalid (a
// programming error, not a runtime condition).
func NewTrackersHandler(cfg ConfigSnapshot) *TrackersHandler {
	tmpl := template.Must(template.New("trackers").Parse(shellTemplates + trackersTemplate))
	return &TrackersHandler{tmpl: tmpl, cfg: cfg}
}

// trackersViewData is the data passed to the Trackers page template: the
// shared shell (R6) plus the configured-tracker rows (R5).
type trackersViewData struct {
	shellData
	Trackers []trackerView
}

// trackerView is one configured tracker rendered as a row (R5): the repo it
// watches, the file globs it tracks, the tracked fields those globs'
// extractors yield, how often it polls, and the backfill window walked on
// first run.
type trackerView struct {
	Repo           string
	FileGlobs      []string
	TrackedFields  []string
	PollCadence    string
	BackfillWindow string
}

// buildTrackersView maps a config snapshot's resolved trackers into their
// view representation (R5). A nil cfg — the shape a degraded/unavailable
// config read would surface as at this seam — or one with no configured
// trackers yields an empty, non-nil slice (R7) rather than nil, so the
// template's empty-state branch is driven by length alone.
func buildTrackersView(cfg *config.Config) []trackerView {
	if cfg == nil {
		return []trackerView{}
	}

	views := make([]trackerView, 0, len(cfg.TrackerConfigs))
	for _, rt := range cfg.TrackerConfigs {
		views = append(views, trackerView{
			Repo:           rt.Repo,
			FileGlobs:      fileGlobs(rt.Files),
			TrackedFields:  trackedFields(rt.Files),
			PollCadence:    formatCadence(rt.PollIntervalSeconds),
			BackfillWindow: formatBackfillWindow(rt.BackfillDays),
		})
	}
	return views
}

// fileGlobs returns the glob pattern of every FileConfig, in order.
func fileGlobs(files []config.FileConfig) []string {
	globs := make([]string, 0, len(files))
	for _, f := range files {
		globs = append(globs, f.Glob)
	}
	return globs
}

// trackedFields returns the name of every tracked field extracted across
// all of a tracker's files, in order. Duplicates are kept as-is: the same
// field name extracted from two different globs is two distinct tracked
// fields, per the domain glossary ("tracked field = one watched value an
// extractor yields").
func trackedFields(files []config.FileConfig) []string {
	var fields []string
	for _, f := range files {
		for _, field := range f.Fields {
			fields = append(fields, field.Name)
		}
	}
	return fields
}

// formatCadence renders a poll interval in seconds as a short duration
// phrase, e.g. "every 1m0s" or "every 30s".
func formatCadence(seconds int) string {
	return fmt.Sprintf("every %s", time.Duration(seconds)*time.Second)
}

// formatBackfillWindow renders a backfill window in days as a short,
// singular/plural phrase, e.g. "1 day" or "7 days" ("0 days" means no
// backfill — only commits from the first poll onward are tracked).
func formatBackfillWindow(days int) string {
	return pluralUnit(days, "day")
}

// ServeHTTP satisfies http.Handler.
func (h *TrackersHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := trackersViewData{
		shellData: buildShell(r.URL.Path, trackersTitle, trackersSubtitle, ""),
		Trackers:  buildTrackersView(h.cfg.Current()),
	}
	if err := h.tmpl.Execute(w, data); err != nil {
		// The response may already be partly written, so we can't change the
		// status code here — just record the failure so it's observable.
		log.Printf("web: render trackers template: %v", err)
	}
}
