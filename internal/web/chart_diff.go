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
	"net/http"
	"path"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
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
	// Security headers apply to every response regardless of representation or
	// status, so they are set before anything can return.
	setSecurityHeaders(w.Header())

	// Negotiation depends only on the Accept header, so it is decided up front
	// and every exit path below — including the error paths — honors it.
	// Content-Type is deliberately NOT set here: it is set by whichever
	// renderer actually runs, so it can never describe a body that was not
	// produced.
	json := wantsJSON(r.Header.Get("Accept"))

	repo := r.URL.Query().Get("repo")
	commitSha := r.URL.Query().Get("commitSha")
	tenantPath := r.URL.Query().Get("path")
	if repo == "" || commitSha == "" || tenantPath == "" {
		writeDetailError(r, w, json, http.StatusBadRequest, genericBadRequestMsg)
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
	// changeset — same writeDiffNotFound call, no distinguishing signal, in
	// whichever representation was negotiated — so a caller can't tell
	// "unknown commit" apart from "known commit, wrong path", and can't learn
	// the difference by switching Accept headers either.
	logger := telemetry.LoggerFromContext(r.Context())

	var cs changeset.Changeset
	var found bool
	err := telemetry.WithSpan(r.Context(), tracer, "store.get_changeset", func(context.Context) error {
		var err error
		cs, found, err = h.checker.GetChangeset(repo, commitSha)
		return err
	})
	if err != nil {
		logger.Error("web: check changeset existence for chart diff", "repo", repo, "tenant", tenantPath, "commitSha", commitSha, "error", err)
		writeDetailError(r, w, json, http.StatusInternalServerError, genericServerErrorMsg)
		return
	}
	if !found || !hasChartChangeAt(cs, tenantPath) {
		writeDiffNotFound(r, w, json)
		return
	}

	var chartRepo chartdiff.ChartRepo
	err = telemetry.WithSpan(r.Context(), tracer, "gitsource.resolve_chart_repo", func(context.Context) error {
		var err error
		chartRepo, err = h.resolver.ResolveChartRepo(repo)
		return err
	})
	if err != nil {
		logger.Error("web: resolve chart repo for chart diff", "repo", repo, "tenant", tenantPath, "commitSha", commitSha, "error", err)
		writeDetailError(r, w, json, http.StatusInternalServerError, genericServerErrorMsg)
		return
	}

	outcome := h.engine.Diff(r.Context(), chartRepo, chartdiff.Request{
		RepoName:   repo,
		TenantPath: tenantPath,
		CommitSha:  commitSha,
	})

	if json {
		writeJSON(r, w, http.StatusOK, toChartDiffJSON(outcome))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderChartDiff(w, outcome); err != nil {
		logResponseWriteError(r.Context(), "web: render chart diff", err, "repo", repo, "tenant", tenantPath, "commitSha", commitSha)
	}
}

// hasChartChangeAt reports whether cs contains at least one chart-kind Change
// (Kind == changeset.KindChart) whose own source file directory equals
// tenantPath. This is the request's actual authorization check: (repo,
// commitSha) being a real, ingested Changeset is necessary but not
// sufficient, since a single commit's Changeset can carry Changes for many
// tenants — tenantPath must name a directory this specific changeset actually
// recorded a chart change for.
//
// The directory is taken with path.Dir, not filepath.Dir: Change.FilePath is a
// git path, which is forward-slash separated on every platform. filepath.Dir
// would rewrite the separator to "\" on Windows, so a caller sending the
// documented forward-slash spelling would be refused — indistinguishably from
// an unknown changeset, and so with no signal about why. See
// changeset_detail_render.go, which must derive tenantPath the same way.
func hasChartChangeAt(cs changeset.Changeset, tenantPath string) bool {
	for _, c := range cs.Changes {
		if c.Kind == changeset.KindChart && path.Dir(c.FilePath) == tenantPath {
			return true
		}
	}
	return false
}
