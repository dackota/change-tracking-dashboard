// Package web (this file): the GET /trackers page (R5, R12). It renders the
// configured trackers — a tracker is a (repo, file-glob, extractor) triple
// per the domain glossary — read from the live, hot-reloaded config
// snapshot: for each one, its repo, the file globs it watches, the tracked
// fields (one watched value per extractor) those globs yield, how often it
// polls, the backfill window walked on first run, and its poll-health status
// (last success, last error, next run — R12) joined in from the pollstatus
// registry. Renders from the same shared shell (sidebar + header) every page
// handler builds via buildShell (R6).
package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/pollstatus"
)

// maxPollErrorDisplayLen bounds how much of a tracker's raw poll-error text
// (see pollstatus.TrackerStatus.LastError's doc comment — it's an unbounded,
// verbatim Go error string that may embed a subprocess's full stderr output)
// is ever rendered in the Trackers view. This is defense-in-depth, not the
// escaping itself: html/template's default auto-escaping (never
// template.HTML) is what prevents an XSS/markup-injection path here;
// truncation only bounds the size of what an unauthenticated viewer sees.
const maxPollErrorDisplayLen = 160

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

// TrackersHandler serves the Trackers page at GET /trackers (R5, R12).
type TrackersHandler struct {
	tmpl       *template.Template
	cfg        ConfigSnapshot
	pollStatus PollHealthSnapshot
}

// NewTrackersHandler creates a TrackersHandler backed by cfg (the tracker
// config snapshot) and pollStatus (the poll-health registry, used for both
// the shared header's chip and this page's own per-tracker status columns,
// R12), and pre-parses the page template. Panics if the embedded template is
// invalid (a programming error, not a runtime condition).
func NewTrackersHandler(cfg ConfigSnapshot, pollStatus PollHealthSnapshot) *TrackersHandler {
	tmpl := template.Must(template.New("trackers").Parse(shellTemplates + trackersTemplate))
	return &TrackersHandler{tmpl: tmpl, cfg: cfg, pollStatus: pollStatus}
}

// trackersViewData is the data passed to the Trackers page template: the
// shared shell (R6, carrying the poll-status chip — R11) plus the
// configured-tracker rows (R5, R12).
type trackersViewData struct {
	shellData
	Trackers []trackerView
}

// trackerView is one configured tracker rendered as a row (R5, R12): the
// repo it watches, the file globs it tracks, the tracked fields those
// globs' extractors yield, how often it polls, the backfill window walked
// on first run, and its poll-health status columns — last success, last
// error, next run.
type trackerView struct {
	Repo           string
	FileGlobs      []string
	TrackedFields  []string
	PollCadence    string
	BackfillWindow string

	// LastSuccess is a ready-to-render relative phrase, e.g. "2 minutes
	// ago", or "never" if this repo has no recorded successful poll.
	LastSuccess string
	// LastError is the most recent poll-failure text (verbatim from
	// pollstatus.TrackerStatus.LastError) for this repo, or "" when its
	// most recent poll attempt succeeded or it has never been attempted.
	// html/template auto-escapes it wherever the template renders it — it
	// must never be wrapped in template.HTML (see pollstatus's doc comment:
	// this text can carry internal detail from a raw Go error).
	LastError string
	// NextRun is a ready-to-render relative phrase, e.g. "in 3 minutes",
	// "due now", or "unknown" if this repo has never been attempted.
	NextRun string
}

// buildTrackersView maps a config snapshot's resolved trackers into their
// view representation (R5), joined with poll-health status columns from a
// pollstatus snapshot (R12). now is threaded through explicitly (never
// time.Now() here) so the relative-phrase computations stay testable
// against a fixed clock. A nil cfg — the shape a degraded/unavailable
// config read would surface as at this seam — or one with no configured
// trackers yields an empty, non-nil slice (R7) rather than nil, so the
// template's empty-state branch is driven by length alone.
func buildTrackersView(cfg *config.Config, snapshot []pollstatus.TrackerStatus, now time.Time) []trackerView {
	if cfg == nil {
		return []trackerView{}
	}

	views := make([]trackerView, 0, len(cfg.TrackerConfigs))
	for _, rt := range cfg.TrackerConfigs {
		status := buildTrackerStatusView(rt.Repo, snapshot, now)
		views = append(views, trackerView{
			Repo:           rt.Repo,
			FileGlobs:      fileGlobs(rt.Files),
			TrackedFields:  trackedFields(rt.Files),
			PollCadence:    formatCadence(rt.PollIntervalSeconds),
			BackfillWindow: formatBackfillWindow(rt.BackfillDays),
			LastSuccess:    status.LastSuccess,
			LastError:      status.LastError,
			NextRun:        status.NextRun,
		})
	}
	return views
}

// trackerStatusView is the poll-health columns for a single Trackers-view
// row (R12), before being folded into trackerView.
type trackerStatusView struct {
	LastSuccess string
	LastError   string
	NextRun     string
}

// buildTrackerStatusView aggregates every pollstatus entry belonging to repo
// — a repo's config entry can cover several (file-glob, field) trackers,
// each recorded as its own pollstatus entry — into one row's status columns,
// using the same reduction (aggregatePollHealth) the header chip builds on:
// the most recent success, the most recent error, and the soonest next run
// across that repo's trackers. A repo with no matching entry (never polled)
// degrades to "never"/""/"unknown" rather than a zero-time artifact.
func buildTrackerStatusView(repo string, snapshot []pollstatus.TrackerStatus, now time.Time) trackerStatusView {
	var matching []pollstatus.TrackerStatus
	for _, ts := range snapshot {
		if ts.Repo == repo {
			matching = append(matching, ts)
		}
	}

	view := trackerStatusView{LastSuccess: "never", NextRun: "unknown"}
	if len(matching) == 0 {
		return view
	}

	agg := aggregatePollHealth(matching)
	if !agg.LatestSuccess.IsZero() {
		view.LastSuccess = humanizeRelative(agg.LatestSuccess, now)
	}
	view.LastError = truncateErrorText(agg.LatestError, maxPollErrorDisplayLen)
	if !agg.SoonestNextRun.IsZero() {
		view.NextRun = humanizeUntil(agg.SoonestNextRun, now)
	}
	return view
}

// truncateErrorText caps s to at most max runes, appending an ellipsis
// marker when truncated. Rune-based (not byte-based) so a multi-byte UTF-8
// character is never split mid-sequence.
func truncateErrorText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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

	now := time.Now()
	snapshot := h.pollStatus.Snapshot()
	data := trackersViewData{
		shellData: buildShell(r.URL.Path, trackersTitle, trackersSubtitle, "", statusChip(snapshot, now)),
		Trackers:  buildTrackersView(h.cfg.Current(), snapshot, now),
	}
	if err := h.tmpl.Execute(w, data); err != nil {
		// The response may already be partly written, so we can't change the
		// status code here — just record the failure so it's observable.
		log.Printf("web: render trackers template: %v", err)
	}
}
