// Package web (this file): the GET /api/changesets/detail/plan-diff
// endpoint. It computes (or retrieves from cache) a static Terraform
// plan-diff for a Terraform-kind Change and renders it as a server-rendered,
// escaped HTML fragment for the Terraform resource-change detail slot — a
// separate endpoint from the per-kind detail (changeset_detail.go) and from
// the sibling chart-diff endpoint (chart_diff.go), mirroring that endpoint's
// shape and security posture exactly (acceptance criterion 8: "wire into
// whatever UI/API surface the existing chartdiff resource-change view uses
// for Helm charts").
package web

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// PlanDiffEngine computes a static Terraform plan-diff Outcome for a
// Terraform-kind Change. *plandiff.Engine satisfies this directly; tests
// inject a fake to exercise each classified message without real HCL
// parsing.
type PlanDiffEngine interface {
	Diff(ctx context.Context, repo plandiff.PlanRepo, req plandiff.Request) plandiff.Outcome
}

// PlanRepoResolver resolves a repo name (as carried on a Change/Changeset)
// to a plandiff.PlanRepo for a single plan-diff computation. Mirrors
// ChartRepoResolver's identical role for the sibling chart-diff endpoint.
type PlanRepoResolver interface {
	ResolvePlanRepo(repo string) (plandiff.PlanRepo, error)
}

// PlanDiffHandler serves GET /api/changesets/detail/plan-diff as a
// server-rendered HTML fragment.
type PlanDiffHandler struct {
	engine   PlanDiffEngine
	resolver PlanRepoResolver
	checker  ChangesetExistenceChecker
}

// NewPlanDiffHandler creates a PlanDiffHandler backed by engine, resolver,
// and checker. checker gates every request: resolver/engine only ever run
// for a (repo, commitSha) pair checker confirms is an already-ingested
// Changeset that also carries a KindTerraform Change at path — mirroring
// ChartDiffHandler's identical two-part security gate (existence +
// path-scoped Kind match) for the sibling endpoint.
func NewPlanDiffHandler(engine PlanDiffEngine, resolver PlanRepoResolver, checker ChangesetExistenceChecker) *PlanDiffHandler {
	return &PlanDiffHandler{engine: engine, resolver: resolver, checker: checker}
}

// ServeHTTP satisfies http.Handler.
func (h *PlanDiffHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	repo := r.URL.Query().Get("repo")
	commitSha := r.URL.Query().Get("commitSha")
	path := r.URL.Query().Get("path")
	if repo == "" || commitSha == "" || path == "" {
		http.Error(w, genericBadRequestMsg, http.StatusBadRequest)
		return
	}

	logger := telemetry.LoggerFromContext(r.Context())

	// Security gate: repo/commitSha/path are unauthenticated, caller-supplied
	// input — see ChartDiffHandler.ServeHTTP's identical rationale. Only
	// proceed to ResolvePlanRepo (and the git clone/fetch/PlainOpen it can
	// trigger) once (repo, commitSha) is confirmed a real, already-ingested
	// Changeset carrying a KindTerraform Change whose own directory is path.
	var cs changeset.Changeset
	var found bool
	err := telemetry.WithSpan(r.Context(), tracer, "store.get_changeset", func(context.Context) error {
		var err error
		cs, found, err = h.checker.GetChangeset(repo, commitSha)
		return err
	})
	if err != nil {
		logger.Error("web: check changeset existence for plan diff", "repo", repo, "tenant", path, "commitSha", commitSha, "error", err)
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}
	if !found || !hasTerraformChangeAt(cs, path) {
		http.NotFound(w, r)
		return
	}

	var planRepo plandiff.PlanRepo
	err = telemetry.WithSpan(r.Context(), tracer, "gitsource.resolve_plan_repo", func(context.Context) error {
		var err error
		planRepo, err = h.resolver.ResolvePlanRepo(repo)
		return err
	})
	if err != nil {
		logger.Error("web: resolve plan repo for plan diff", "repo", repo, "tenant", path, "commitSha", commitSha, "error", err)
		http.Error(w, genericServerErrorMsg, http.StatusInternalServerError)
		return
	}

	// Acceptance criterion 9: the detail route wraps the diff itself in its
	// own span (distinct from — and in addition to — the child spans
	// Engine.Diff's own call graph already emits internally), recording an
	// error status for a classified failure Kind. The synthesized error
	// passed to WithSpan carries only the already-safe, already-classified
	// Kind string — never any internal cause — so the span's exception/
	// status text can never leak git/HCL-parser detail either.
	var outcome plandiff.Outcome
	_ = telemetry.WithSpan(r.Context(), tracer, "plandiff.diff", func(ctx context.Context) error {
		outcome = h.engine.Diff(ctx, planRepo, plandiff.Request{
			RepoName:   repo,
			TenantPath: path,
			CommitSha:  commitSha,
		})
		return planDiffSpanError(outcome.Kind)
	})

	if err := renderPlanDiff(w, outcome); err != nil {
		logger.Error("web: render plan diff", "repo", repo, "tenant", path, "commitSha", commitSha, "error", err)
	}
}

// planDiffSpanError returns a non-nil error for a plandiff.Kind that
// represents a genuine failure (CouldNotRender, ExceededLimits), so
// telemetry.WithSpan records it as a span exception with Error status.
// OK and NoPriorVersion are both expected, successful classifications — no
// error is recorded for either.
func planDiffSpanError(kind plandiff.Kind) error {
	switch kind {
	case plandiff.OK, plandiff.NoPriorVersion:
		return nil
	default:
		return fmt.Errorf("plandiff: outcome %s", kind)
	}
}

// hasTerraformChangeAt reports whether cs contains at least one
// Terraform-kind Change (Kind == changeset.KindTerraform) whose own source
// file directory equals path. Mirrors hasChartChangeAt's identical
// authorization role for the sibling chart-diff endpoint.
func hasTerraformChangeAt(cs changeset.Changeset, path string) bool {
	for _, c := range cs.Changes {
		if c.Kind == changeset.KindTerraform && filepath.Dir(c.FilePath) == path {
			return true
		}
	}
	return false
}
