// Package web (this file): the GET /api/changesets/detail/chart-diff
// endpoint. It computes (or retrieves from cache) a Chart diff for a
// chart-kind Change and renders it as a server-rendered, escaped HTML
// fragment for the chart-change detail slot — a separate endpoint from the
// per-kind detail (changeset_detail.go) so a slow or bounded-out render
// never blocks the rest of the detail view (PRD: "a new endpoint, separate
// from the per-kind detail so a slow render never blocks it").
package web

import (
	"context"
	"log"
	"net/http"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
)

// ChartDiffEngine computes a Chart diff Outcome for a chart-kind Change.
// *chartdiff.Engine satisfies this directly; tests inject a fake to exercise
// each classified message without a real Helm render.
type ChartDiffEngine interface {
	Diff(ctx context.Context, repo chartdiff.ChartRepo, req chartdiff.Request) chartdiff.Outcome
}

// ChartRepoResolver resolves a repo name (as carried on a Change/Changeset)
// to a chartdiff.ChartRepo for a single Chart diff computation.
// cmd/dashboard's sourceCache satisfies this via a small adapter over its
// existing get method — defined here, at the point of use, per this
// project's small-interfaces convention.
type ChartRepoResolver interface {
	ResolveChartRepo(repo string) (chartdiff.ChartRepo, error)
}

// ChartDiffHandler serves GET /api/changesets/detail/chart-diff as a
// server-rendered HTML fragment.
type ChartDiffHandler struct {
	engine   ChartDiffEngine
	resolver ChartRepoResolver
}

// NewChartDiffHandler creates a ChartDiffHandler backed by engine and
// resolver.
func NewChartDiffHandler(engine ChartDiffEngine, resolver ChartRepoResolver) *ChartDiffHandler {
	return &ChartDiffHandler{engine: engine, resolver: resolver}
}

// ServeHTTP satisfies http.Handler.
func (h *ChartDiffHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	repo := r.URL.Query().Get("repo")
	commitSha := r.URL.Query().Get("commitSha")
	path := r.URL.Query().Get("path")
	if repo == "" || commitSha == "" || path == "" {
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}

	chartRepo, err := h.resolver.ResolveChartRepo(repo)
	if err != nil {
		log.Printf("web: resolve chart repo %q for chart diff (tenant=%q commit=%q): %v", repo, path, commitSha, err)
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}

	outcome := h.engine.Diff(r.Context(), chartRepo, chartdiff.Request{
		RepoName:   repo,
		TenantPath: path,
		CommitSha:  commitSha,
	})

	if err := renderChartDiff(w, outcome); err != nil {
		log.Printf("web: render chart diff repo=%q tenant=%q commit=%q: %v", repo, path, commitSha, err)
	}
}
