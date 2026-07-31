package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// tenantPathAttrRE extracts the data-tenant-path attribute the detail view
// renders into a diff slot — the exact string timeline.js reads and puts in
// its fetch URL.
var tenantPathAttrRE = regexp.MustCompile(`data-tenant-path="([^"]*)"`)

// renderedTenantPath renders the changeset detail view for a single Change at
// filePath and returns the data-tenant-path the diff slot carries.
func renderedTenantPath(t *testing.T, filePath, commitSha string) string {
	t.Helper()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    filePath,
		Field:       "f",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("old"),
		NewValue:    ptr("new"),
		CommitSha:   commitSha,
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=infra-repo&commitSha="+commitSha, nil)
	rr := httptest.NewRecorder()
	web.NewChangesetDetailHandler(st).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	m := tenantPathAttrRE.FindStringSubmatch(rr.Body.String())
	if m == nil {
		t.Fatalf("detail HTML carries no data-tenant-path attribute; body: %s", rr.Body.String())
	}
	return m[1]
}

// TestTenantPath_RoundTripsFromDetailRenderIntoChartDiffGate closes the loop
// the two halves of the tenant-path contract form: the detail view derives
// data-tenant-path from a Change's FilePath, timeline.js reads that attribute
// and puts it in the chart-diff fetch URL, and the chart-diff handler's
// security gate compares it back against the same FilePath.
//
// Testing the halves separately is not enough — they can agree with each
// other while both being wrong (as they did on Windows, where both derived
// "envs\prod" from a git path that is always forward-slash separated). This
// test pins the value itself as well as the round-trip, so a regression in
// either half fails, and so does a regression in both.
//
// A multi-segment path is the point: a single-segment path has no separator
// and so cannot detect the bug at all.
func TestTenantPath_RoundTripsFromDetailRenderIntoChartDiffGate(t *testing.T) {
	t.Parallel()

	const filePath = "workloads/team-a/app/Chart.yaml"
	const wantTenantPath = "workloads/team-a/app"

	got := renderedTenantPath(t, filePath, "commit-chart-roundtrip")
	if got != wantTenantPath {
		t.Errorf("rendered data-tenant-path = %q, want %q (a git path is always forward-slash separated, on every platform)", got, wantTenantPath)
	}

	// Whatever the view rendered must be accepted by the gate that guards the
	// endpoint the view points at.
	cs := changeset.Changeset{Changes: []changeset.Change{
		{Change: domain.Change{FilePath: filePath}, Kind: changeset.KindChart},
	}}
	h := web.NewChartDiffHandler(
		&fakeChartDiffEngine{fn: func(context.Context, chartdiff.ChartRepo, chartdiff.Request) chartdiff.Outcome {
			return chartdiff.Outcome{Kind: chartdiff.OK}
		}},
		&fakeChartRepoResolver{fn: func(string) (chartdiff.ChartRepo, error) { return stubChartRepo{}, nil }},
		&fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
			return cs, true, nil
		}},
	)

	rr := serveChartDiff(h, "/api/changesets/detail/chart-diff?repo=r&commitSha=sha&path="+url.QueryEscape(got))
	if rr.Code != http.StatusOK {
		t.Errorf("chart-diff gate rejected the tenant path the detail view itself rendered (%q): status = %d, want 200; body: %s",
			got, rr.Code, rr.Body.String())
	}
}

// TestTenantPath_RoundTripsFromDetailRenderIntoPlanDiffGate is the Terraform
// twin — same contract, same reason, for the plan-diff slot and endpoint.
func TestTenantPath_RoundTripsFromDetailRenderIntoPlanDiffGate(t *testing.T) {
	t.Parallel()

	const filePath = "envs/prod/network/main.tf"
	const wantTenantPath = "envs/prod/network"

	got := renderedTenantPath(t, filePath, "commit-tf-roundtrip")
	if got != wantTenantPath {
		t.Errorf("rendered data-tenant-path = %q, want %q (a git path is always forward-slash separated, on every platform)", got, wantTenantPath)
	}

	cs := changeset.Changeset{Changes: []changeset.Change{
		{Change: domain.Change{FilePath: filePath}, Kind: changeset.KindResource},
	}}
	h := web.NewPlanDiffHandler(
		&fakePlanDiffEngine{fn: func(context.Context, plandiff.PlanRepo, plandiff.Request) plandiff.Outcome {
			return plandiff.Outcome{Kind: plandiff.OK}
		}},
		&fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return stubPlanRepo{}, nil }},
		&fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
			return cs, true, nil
		}},
	)

	rr := servePlanDiff(h, "/api/changesets/detail/plan-diff?repo=r&commitSha=sha&path="+url.QueryEscape(got))
	if rr.Code != http.StatusOK {
		t.Errorf("plan-diff gate rejected the tenant path the detail view itself rendered (%q): status = %d, want 200; body: %s",
			got, rr.Code, rr.Body.String())
	}
}

// TestTenantPath_DocumentedAPIPathIsAccepted pins the external contract
// directly: the README documents forward-slash tenant paths (path=envs/prod)
// for both diff endpoints, and an API client following that documentation
// must be let through on every platform.
//
// The round-trip tests above would still pass if both halves drifted to some
// other separator in lockstep. This one would not — it hardcodes the
// documented spelling, which is what an external consumer actually sends and
// has no way to vary. The endpoint's 404 is deliberately indistinguishable
// from an unknown changeset, so such a client gets no signal about what went
// wrong.
func TestTenantPath_DocumentedAPIPathIsAccepted(t *testing.T) {
	t.Parallel()

	cs := changeset.Changeset{Changes: []changeset.Change{
		{Change: domain.Change{FilePath: "envs/prod/main.tf"}, Kind: changeset.KindResource},
	}}
	h := web.NewPlanDiffHandler(
		&fakePlanDiffEngine{fn: func(context.Context, plandiff.PlanRepo, plandiff.Request) plandiff.Outcome {
			return plandiff.Outcome{Kind: plandiff.OK}
		}},
		&fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return stubPlanRepo{}, nil }},
		&fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
			return cs, true, nil
		}},
	)

	rr := servePlanDiff(h, "/api/changesets/detail/plan-diff?repo=r&commitSha=sha&path=envs/prod")
	if rr.Code != http.StatusOK {
		t.Errorf("the README's documented path spelling (envs/prod) was rejected: status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}
