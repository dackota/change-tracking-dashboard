// mux.go declares the dashboard's HTTP surface: which routes exist and which
// handler serves each one. It lives here rather than in package main so the
// composed surface is a thing that can be built and exercised in a test —
// before this, nothing outside main could boot the real routing table, and the
// only test of it re-declared the wiring by hand and asserted the two agreed
// in a comment.
package web

import (
	"fmt"
	"net/http"

	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
)

// HealthzRoute is the liveness-probe route pattern. It is exported because a
// caller instrumenting the mux needs the pattern ServeMux recorded in order to
// quiet the probe's request log (see telemetry.WithQuietRoutes) — naming it
// here keeps the registration and that suppression from drifting apart.
const HealthzRoute = "GET /healthz"

// Deps are the collaborators the dashboard's handlers need. Every field is
// required except where noted: NewMux reports what is missing rather than
// building a mux that panics on the first request to the route that needed it.
type Deps struct {
	// Store backs every read of persisted Changes and Changesets.
	Store *store.Store

	// PollHealth supplies the poll-status chip shared by every page, and the
	// Trackers view's per-tracker status columns.
	PollHealth PollHealthSnapshot

	// Config is the live tracker configuration, read per request so a
	// hot-reload is visible without a restart. It also supplies the risk
	// rules; when it does not implement RiskRulesSource, the built-in default
	// rules are used.
	Config ConfigSnapshot

	// ChartDiff and PlanDiff serve the on-demand diff endpoints.
	ChartDiff ChartDiffEngine
	PlanDiff  PlanDiffEngine

	// Repos resolves a repo name to a working copy for the diff endpoints.
	Repos subtree.Resolver
}

// validate reports the first missing dependency, naming the route that would
// have failed without it.
func (d Deps) validate() error {
	switch {
	case d.Store == nil:
		return fmt.Errorf("web: Deps.Store is required (backs /, /api/changesets, /repositories)")
	case d.PollHealth == nil:
		return fmt.Errorf("web: Deps.PollHealth is required (backs the poll-status chip on every page)")
	case d.Config == nil:
		return fmt.Errorf("web: Deps.Config is required (backs /trackers)")
	case d.ChartDiff == nil:
		return fmt.Errorf("web: Deps.ChartDiff is required (backs /api/changesets/detail/chart-diff)")
	case d.PlanDiff == nil:
		return fmt.Errorf("web: Deps.PlanDiff is required (backs /api/changesets/detail/plan-diff)")
	case d.Repos == nil:
		return fmt.Errorf("web: Deps.Repos is required (backs both diff endpoints)")
	}
	return nil
}

// NewMux builds the dashboard's complete routing table.
//
// This is the whole HTTP surface: a caller that serves the returned handler
// serves the dashboard. Cross-cutting concerns are deliberately not applied
// here — RED metrics, tracing and request logging wrap the returned handler at
// the edge, so they cover every route present and future without this
// function knowing about them.
func NewMux(deps Deps) (http.Handler, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}

	// The risk rules ride along with the config when it can supply them. A
	// ConfigSnapshot that cannot is not an error: handlers fall back to the
	// built-in default rules (see riskRulesOrDefault).
	var changesetsOpts []ChangesetsOption
	var detailOpts []ChangesetDetailOption
	if rules, ok := deps.Config.(RiskRulesSource); ok {
		changesetsOpts = append(changesetsOpts, WithChangesetsRiskRules(rules))
		detailOpts = append(detailOpts, WithDetailRiskRules(rules))
	}

	mux := http.NewServeMux()
	mux.Handle("/", NewTimelineHandler(deps.Store, deps.PollHealth))
	mux.Handle("/static/", NewStaticHandler())
	mux.Handle("/api/changesets", NewChangesetsHandler(deps.Store, changesetsOpts...))
	mux.Handle("/api/changesets/detail", NewChangesetDetailHandler(deps.Store, detailOpts...))
	mux.Handle("/api/changesets/detail/chart-diff", NewChartDiffHandler(deps.ChartDiff, deps.Repos, deps.Store))
	mux.Handle("/api/changesets/detail/plan-diff", NewPlanDiffHandler(deps.PlanDiff, deps.Repos, deps.Store))
	mux.Handle("GET /trackers", NewTrackersHandler(deps.Config, deps.PollHealth))
	mux.Handle("GET /repositories", NewRepositoriesHandler(deps.Store, deps.PollHealth))
	mux.Handle("GET /changes", NewChangesHandler(deps.PollHealth))
	mux.Handle(HealthzRoute, NewHealthzHandler())
	return mux, nil
}
