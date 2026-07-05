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
	"path/filepath"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
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

// ChangesetExistenceChecker reports whether (repo, commitSha) is a real,
// already-ingested Changeset. *store.Store satisfies this via its existing
// GetChangeset method.
//
// This is the endpoint's security boundary: repo and commitSha arrive on an
// unauthenticated HTTP request, so they must never reach ChartRepoResolver
// (and, behind it, cmd/dashboard's sourceCache — which clones/fetches
// arbitrary git URLs, attaches the live GitHub App installation token to
// "https://" URLs, and PlainOpens arbitrary local paths) unless the pair is
// one the poller itself has already legitimately polled. Mirrors
// changeset_detail.go's own GetChangeset gate for the sibling endpoint.
type ChangesetExistenceChecker interface {
	GetChangeset(repo, commitSha string) (changeset.Changeset, bool, error)
}

// ChartDiffHandler serves GET /api/changesets/detail/chart-diff as a
// server-rendered HTML fragment.
type ChartDiffHandler struct {
	engine   ChartDiffEngine
	resolver ChartRepoResolver
	checker  ChangesetExistenceChecker
}

// NewChartDiffHandler creates a ChartDiffHandler backed by engine, resolver,
// and checker. checker gates every request: resolver/engine only ever run
// for a (repo, commitSha) pair checker confirms is an already-ingested
// Changeset.
func NewChartDiffHandler(engine ChartDiffEngine, resolver ChartRepoResolver, checker ChangesetExistenceChecker) *ChartDiffHandler {
	return &ChartDiffHandler{engine: engine, resolver: resolver, checker: checker}
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

	// Security gate: repo/commitSha are unauthenticated, caller-supplied
	// input. Only proceed to ResolveChartRepo (and the git clone/fetch/
	// PlainOpen it can trigger) once we've confirmed this exact pair is a
	// Changeset the poller already ingested from trusted tracker config —
	// never a repo string an attacker invented on the request. That alone is
	// not sufficient authorization for path, though: a single commit's
	// Changeset can span many tenants (domain.Change.FilePath is
	// multi-tenant within one repo), so path must additionally match the
	// directory of one of this changeset's own chart-kind Changes — the same
	// directory the chart-change detail slot that requests this diff was
	// itself rendered from (see changeset_detail_render.go's TenantPath). A
	// path with no matching chart-kind Change (wrong tenant, a value-only
	// change, or nothing at all) is rejected exactly like an unknown
	// changeset — same http.NotFound call, no distinguishing signal — so a
	// caller can't tell "unknown commit" apart from "known commit, wrong
	// path".
	cs, found, err := h.checker.GetChangeset(repo, commitSha)
	if err != nil {
		log.Printf("web: check changeset existence for chart diff repo=%q (tenant=%q commit=%q): %v", repo, path, commitSha, err)
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}
	if !found || !hasChartChangeAt(cs, path) {
		http.NotFound(w, r)
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

// hasChartChangeAt reports whether cs contains at least one chart-kind Change
// (Kind == changeset.KindChart) whose own source file directory equals path.
// This is the request's actual authorization check: (repo, commitSha) being
// a real, ingested Changeset is necessary but not sufficient, since a single
// commit's Changeset can carry Changes for many tenants — path must name a
// directory this specific changeset actually recorded a chart change for.
func hasChartChangeAt(cs changeset.Changeset, path string) bool {
	for _, c := range cs.Changes {
		if c.Kind == changeset.KindChart && filepath.Dir(c.FilePath) == path {
			return true
		}
	}
	return false
}
