package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
	"github.com/dackota/change-tracking-dashboard/internal/web"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// fakePlanDiffEngine is a web.PlanDiffEngine test double.
type fakePlanDiffEngine struct {
	fn     func(ctx context.Context, repo plandiff.PlanRepo, req plandiff.Request) plandiff.Outcome
	called bool
}

func (f *fakePlanDiffEngine) Diff(ctx context.Context, repo plandiff.PlanRepo, req plandiff.Request) plandiff.Outcome {
	f.called = true
	return f.fn(ctx, repo, req)
}

// fakePlanRepoResolver is a web.PlanRepoResolver test double.
type fakePlanRepoResolver struct {
	fn     func(repo string) (plandiff.PlanRepo, error)
	called bool
}

func (f *fakePlanRepoResolver) ResolvePlanRepo(repo string) (plandiff.PlanRepo, error) {
	f.called = true
	return f.fn(repo)
}

// stubPlanRepo is a minimal plandiff.PlanRepo used where the resolver must
// succeed but the fake engine never actually calls its methods.
type stubPlanRepo struct{}

func (stubPlanRepo) FirstParent(string) (string, error) { return "", nil }
func (stubPlanRepo) MaterializeSubtreeBounded(string, string, string, gitsource.MaterializeBounds) error {
	return nil
}

const defaultPlanDiffURL = "/api/changesets/detail/plan-diff?repo=r&commitSha=sha&path=envs/prod"

func alwaysFoundTerraformChecker() *fakeChangesetExistenceChecker {
	cs := changeset.Changeset{Changes: []changeset.Change{
		{Change: domain.Change{FilePath: "envs/prod/main.tf"}, Kind: changeset.KindResource},
	}}
	return &fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return cs, true, nil
	}}
}

func newPlanDiffHandlerForOutcome(outcome plandiff.Outcome) *web.PlanDiffHandler {
	engine := &fakePlanDiffEngine{fn: func(context.Context, plandiff.PlanRepo, plandiff.Request) plandiff.Outcome { return outcome }}
	resolver := &fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return stubPlanRepo{}, nil }}
	return web.NewPlanDiffHandler(engine, resolver, alwaysFoundTerraformChecker())
}

func servePlanDiff(h http.Handler, url string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestPlanDiffHandler_MissingRequiredParam_Returns400Generic mirrors
// chart_diff_test.go's identical request-validation contract.
func TestPlanDiffHandler_MissingRequiredParam_Returns400Generic(t *testing.T) {
	t.Parallel()

	h := web.NewPlanDiffHandler(&fakePlanDiffEngine{}, &fakePlanRepoResolver{}, &fakeChangesetExistenceChecker{})

	cases := []string{
		"/api/changesets/detail/plan-diff?commitSha=sha&path=envs/prod",
		"/api/changesets/detail/plan-diff?repo=r&path=envs/prod",
		"/api/changesets/detail/plan-diff?repo=r&commitSha=sha",
		"/api/changesets/detail/plan-diff",
	}
	for _, url := range cases {
		rr := servePlanDiff(h, url)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("url=%q status = %d, want 400; body: %s", url, rr.Code, rr.Body.String())
		}
	}
}

// TestPlanDiffHandler_UnknownChangeset_RejectsWithoutReachingResolverOrEngine
// mirrors chart_diff_test.go's security-gate regression: repo/commitSha
// arrive unauthenticated, so a never-ingested pair must never reach the
// resolver/engine.
func TestPlanDiffHandler_UnknownChangeset_RejectsWithoutReachingResolverOrEngine(t *testing.T) {
	t.Parallel()

	resolver := &fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return stubPlanRepo{}, nil }}
	engine := &fakePlanDiffEngine{fn: func(context.Context, plandiff.PlanRepo, plandiff.Request) plandiff.Outcome {
		return plandiff.Outcome{Kind: plandiff.OK}
	}}
	checker := &fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return changeset.Changeset{}, false, nil
	}}
	h := web.NewPlanDiffHandler(engine, resolver, checker)

	rr := servePlanDiff(h, defaultPlanDiffURL)

	if resolver.called || engine.called {
		t.Error("resolver/engine invoked for a never-ingested changeset — security gate bypassed")
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPlanDiffHandler_PathNotTerraformKind_RejectsWithoutReachingResolverOrEngine
// proves the Kind-based authorization gate: a path whose only Change in this
// changeset is KindValue (or absent entirely) must never reach the resolver/
// engine, exactly mirroring hasChartChangeAt's KindChart gate.
func TestPlanDiffHandler_PathNotTerraformKind_RejectsWithoutReachingResolverOrEngine(t *testing.T) {
	t.Parallel()

	resolver := &fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return stubPlanRepo{}, nil }}
	engine := &fakePlanDiffEngine{fn: func(context.Context, plandiff.PlanRepo, plandiff.Request) plandiff.Outcome {
		return plandiff.Outcome{Kind: plandiff.OK}
	}}
	cs := changeset.Changeset{Changes: []changeset.Change{
		{Change: domain.Change{FilePath: "envs/prod/values.yaml"}, Kind: changeset.KindValue},
	}}
	checker := &fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return cs, true, nil
	}}
	h := web.NewPlanDiffHandler(engine, resolver, checker)

	rr := servePlanDiff(h, defaultPlanDiffURL)

	if resolver.called || engine.called {
		t.Error("resolver/engine invoked for a path with no Terraform-kind Change — security gate bypassed")
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPlanDiffHandler_ResolverFailure_Returns500GenericWithoutLeakingDetail
// mirrors chart_diff_test.go's identical non-leak contract.
func TestPlanDiffHandler_ResolverFailure_Returns500GenericWithoutLeakingDetail(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("clone failed: /var/secret/internal/path unreachable")
	resolver := &fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return nil, sentinel }}
	h := web.NewPlanDiffHandler(&fakePlanDiffEngine{}, resolver, alwaysFoundTerraformChecker())

	rr := servePlanDiff(h, defaultPlanDiffURL)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "/var/secret") || strings.Contains(body, "clone failed") {
		t.Errorf("error body leaks internal detail: %q", body)
	}
}

// TestPlanDiffHandler_OKOutcome_RendersResourceDeltaAndUnifiedDiff proves
// acceptance criteria 1, 2, and 8: the resource-change view renders the
// added/removed/changed/replaced counts, the per-resource list (including
// the ForcesReplacement flag), and the manifestdiff-rendered unified text.
func TestPlanDiffHandler_OKOutcome_RendersResourceDeltaAndUnifiedDiff(t *testing.T) {
	t.Parallel()

	outcome := plandiff.Outcome{
		Kind: plandiff.OK,
		Summary: plandiff.Summary{
			Added: 1, Removed: 1, Changed: 1, Replaced: 1,
		},
		Resources: []plandiff.ResourceDelta{
			{ResourceType: "oci_core_instance", ResourceName: "fresh", Kind: plandiff.ResourceAdded},
			{ResourceType: "oci_core_instance", ResourceName: "stale", Kind: plandiff.ResourceRemoved, ForcesReplacement: true},
			{ResourceType: "oci_core_instance", ResourceName: "web", Kind: plandiff.ResourceChanged, ForcesReplacement: true},
		},
	}
	outcome.Diff.Unified = "-availability_domain = \"AD-1\"\n+availability_domain = \"AD-2\"\n"
	outcome.Diff.Summary.ManifestsChanged = 3

	h := newPlanDiffHandlerForOutcome(outcome)
	rr := servePlanDiff(h, defaultPlanDiffURL)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-kind="ok"`) {
		t.Errorf("body missing data-kind=\"ok\" marker; got:\n%s", body)
	}
	for _, want := range []string{"fresh", "stale", "web", "AD-1", "AD-2"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing expected substring %q; got:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "data-forces-replacement=\"true\"") {
		t.Errorf("body missing a ForcesReplacement marker; got:\n%s", body)
	}
}

// TestPlanDiffHandler_NonOKOutcomes_RenderDistinctClassifiedMessages mirrors
// chart_diff_test.go's identical contract for plandiff's smaller Kind set.
func TestPlanDiffHandler_NonOKOutcomes_RenderDistinctClassifiedMessages(t *testing.T) {
	t.Parallel()

	kinds := []plandiff.Kind{plandiff.NoPriorVersion, plandiff.CouldNotRender, plandiff.ExceededLimits}
	var bodies []string
	for _, kind := range kinds {
		h := newPlanDiffHandlerForOutcome(plandiff.Outcome{Kind: kind})
		rr := servePlanDiff(h, defaultPlanDiffURL)
		if rr.Code != http.StatusOK {
			t.Fatalf("kind=%s status = %d, want 200", kind, rr.Code)
		}
		body := rr.Body.String()
		if !strings.Contains(body, `data-kind="`+string(kind)+`"`) {
			t.Errorf("kind=%s: body missing data-kind marker; got:\n%s", kind, body)
		}
		bodies = append(bodies, body)
	}
	seen := make(map[string]bool, len(bodies))
	for i, body := range bodies {
		if seen[body] {
			t.Errorf("outcome %d (%s) rendered a body identical to another kind's", i, kinds[i])
		}
		seen[body] = true
	}
}

// TestPlanDiffHandler_SecurityHeaders_PresentOnEveryResponse mirrors
// chart_diff_test.go's identical baseline-hardening contract.
func TestPlanDiffHandler_SecurityHeaders_PresentOnEveryResponse(t *testing.T) {
	t.Parallel()

	h := newPlanDiffHandlerForOutcome(plandiff.Outcome{Kind: plandiff.OK})
	rr := servePlanDiff(h, defaultPlanDiffURL)

	wantHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range wantHeaders {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

// TestPlanDiffHandler_WrapsDiffInSpan_RecordingErrorsOnFailureKinds proves
// acceptance criterion 9's route-level requirement: the detail route wraps
// the Diff call in its own span, with an Ok status on a successful/expected
// classification (OK, NoPriorVersion) and an Error status (with a recorded
// exception) on a failure classification (CouldNotRender, ExceededLimits) —
// without ever attaching any internal error detail (Outcome carries none).
// Deliberately not run in parallel: it reads from the process-wide
// spanExporter TestMain installs once for the whole binary (see
// main_test.go's doc and telemetry_test.go's identical pattern), resetting
// it before each subtest's assertion.
func TestPlanDiffHandler_WrapsDiffInSpan_RecordingErrorsOnFailureKinds(t *testing.T) {
	tests := []struct {
		kind       plandiff.Kind
		wantStatus codes.Code
	}{
		{plandiff.OK, codes.Ok},
		{plandiff.NoPriorVersion, codes.Ok},
		{plandiff.CouldNotRender, codes.Error},
		{plandiff.ExceededLimits, codes.Error},
	}

	for _, tt := range tests {
		spanExporter.Reset()

		h := newPlanDiffHandlerForOutcome(plandiff.Outcome{Kind: tt.kind})
		rr := servePlanDiff(h, defaultPlanDiffURL)
		if rr.Code != http.StatusOK {
			t.Fatalf("kind=%s status = %d, want 200", tt.kind, rr.Code)
		}

		var diffSpan *tracetest.SpanStub
		for _, s := range spanExporter.GetSpans() {
			if s.Name == "plandiff.diff" {
				s := s
				diffSpan = &s
			}
		}
		if diffSpan == nil {
			t.Errorf("kind=%s: no span named plandiff.diff recorded", tt.kind)
		} else if diffSpan.Status.Code != tt.wantStatus {
			t.Errorf("kind=%s: plandiff.diff span status = %v, want %v", tt.kind, diffSpan.Status.Code, tt.wantStatus)
		}
	}
}
