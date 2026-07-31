package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// jsonPlanDiffURL addresses a Terraform change at a tenant path. These tests
// are about Accept negotiation, so the path is incidental — see
// tenant_path_roundtrip_test.go for the tests that pin path handling itself.
const jsonPlanDiffURL = "/api/changesets/detail/plan-diff?repo=r&commitSha=sha&path=prod"

// jsonPlanDiffChecker reports the changeset jsonPlanDiffURL addresses as
// ingested, carrying a Terraform-kind Change at "prod".
func jsonPlanDiffChecker() *fakeChangesetExistenceChecker {
	cs := changeset.Changeset{Changes: []changeset.Change{
		{Change: domain.Change{FilePath: "prod/main.tf"}, Kind: changeset.KindResource},
	}}
	return &fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return cs, true, nil
	}}
}

// newJSONPlanDiffHandler builds a PlanDiffHandler whose engine always returns
// outcome, gated by a checker that accepts jsonPlanDiffURL.
func newJSONPlanDiffHandler(outcome plandiff.Outcome) *web.PlanDiffHandler {
	engine := &fakePlanDiffEngine{fn: func(context.Context, plandiff.PlanRepo, plandiff.Request) plandiff.Outcome {
		return outcome
	}}
	resolver := &fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return stubPlanRepo{}, nil }}
	return web.NewPlanDiffHandler(engine, resolver, jsonPlanDiffChecker())
}

// servePlanDiffAccept issues a GET against a plan-diff handler with the given
// Accept header. An empty accept sends no Accept header at all.
func servePlanDiffAccept(h http.Handler, url, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// okPlanOutcome is a representative successful plan-diff: three resources,
// two of which force replacement (one removal — always destructive — and one
// attribute change that hit a force-replacement attribute).
func okPlanOutcome() plandiff.Outcome {
	return plandiff.Outcome{
		Kind: plandiff.OK,
		Diff: manifestdiff.Result{
			Unified: "-shape = \"VM.Standard2.1\"\n+shape = \"VM.Standard2.4\"\n",
			Summary: manifestdiff.Summary{ManifestsChanged: 3, LinesAdded: 9, LinesRemoved: 4},
		},
		Summary: plandiff.Summary{Added: 1, Removed: 1, Changed: 1, Replaced: 2},
		Resources: []plandiff.ResourceDelta{
			{ResourceType: "oci_core_instance", ResourceName: "web", Kind: plandiff.ResourceChanged, ForcesReplacement: true},
			{ResourceType: "oci_core_vcn", ResourceName: "main", Kind: plandiff.ResourceAdded},
			{ResourceType: "oci_load_balancer", ResourceName: "edge", Kind: plandiff.ResourceRemoved, ForcesReplacement: true},
		},
	}
}

// planDiffBody is the ok-outcome wire shape under test.
type planDiffBody struct {
	Kind string `json:"kind"`
	Diff *struct {
		Unified   string `json:"unified"`
		Truncated bool   `json:"truncated"`
		Summary   struct {
			ManifestsChanged int `json:"manifestsChanged"`
			LinesAdded       int `json:"linesAdded"`
			LinesRemoved     int `json:"linesRemoved"`
		} `json:"summary"`
	} `json:"diff"`
	Summary *struct {
		Added    int `json:"added"`
		Removed  int `json:"removed"`
		Changed  int `json:"changed"`
		Replaced int `json:"replaced"`
	} `json:"summary"`
	Resources []struct {
		Type              string `json:"type"`
		Name              string `json:"name"`
		Kind              string `json:"kind"`
		ForcesReplacement bool   `json:"forcesReplacement"`
	} `json:"resources"`
}

// TestPlanDiffHandler_JSON_OKCarriesDiffTextAndLineSummary is the tracer
// bullet for the plan-diff representation: the unified diff text and its line
// summary reach the wire.
func TestPlanDiffHandler_JSON_OKCarriesDiffTextAndLineSummary(t *testing.T) {
	t.Parallel()

	rr := servePlanDiffAccept(newJSONPlanDiffHandler(okPlanOutcome()), jsonPlanDiffURL, "application/json")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got planDiffBody
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
	}

	if got.Kind != "ok" {
		t.Errorf("kind = %q, want ok", got.Kind)
	}
	if got.Diff == nil {
		t.Fatalf("ok body carries no diff; body: %s", rr.Body.String())
	}
	if got.Diff.Unified != "-shape = \"VM.Standard2.1\"\n+shape = \"VM.Standard2.4\"\n" {
		t.Errorf("diff.unified = %q, want the engine's unified text", got.Diff.Unified)
	}
	if got.Diff.Summary.LinesAdded != 9 || got.Diff.Summary.LinesRemoved != 4 {
		t.Errorf("diff.summary lines = +%d/-%d, want +9/-4", got.Diff.Summary.LinesAdded, got.Diff.Summary.LinesRemoved)
	}
	if got.Diff.Summary.ManifestsChanged != 3 {
		t.Errorf("diff.summary.manifestsChanged = %d, want 3", got.Diff.Summary.ManifestsChanged)
	}
}

// TestPlanDiffHandler_JSON_OKCarriesAggregateResourceCounts verifies the
// aggregate blast-radius counts reach the wire, so a client can render a
// one-line summary ("2 resources force replacement") with no client-side
// computation over the per-resource list.
func TestPlanDiffHandler_JSON_OKCarriesAggregateResourceCounts(t *testing.T) {
	t.Parallel()

	rr := servePlanDiffAccept(newJSONPlanDiffHandler(okPlanOutcome()), jsonPlanDiffURL, "application/json")

	var got planDiffBody
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
	}
	if got.Summary == nil {
		t.Fatalf("ok body carries no summary; body: %s", rr.Body.String())
	}

	if got.Summary.Added != 1 {
		t.Errorf("summary.added = %d, want 1", got.Summary.Added)
	}
	if got.Summary.Removed != 1 {
		t.Errorf("summary.removed = %d, want 1", got.Summary.Removed)
	}
	if got.Summary.Changed != 1 {
		t.Errorf("summary.changed = %d, want 1", got.Summary.Changed)
	}
	// Replaced is a subset of removed+changed, never counted separately from
	// them — so it can exceed neither their sum nor the flagged deltas.
	if got.Summary.Replaced != 2 {
		t.Errorf("summary.replaced = %d, want 2", got.Summary.Replaced)
	}
	if got.Summary.Replaced > got.Summary.Removed+got.Summary.Changed {
		t.Errorf("summary.replaced (%d) exceeds removed+changed (%d); replaced must be a subset",
			got.Summary.Replaced, got.Summary.Removed+got.Summary.Changed)
	}
}

// TestPlanDiffHandler_JSON_OKCarriesPerResourceDeltasInSortedOrder verifies
// each resource's type, name, change kind, and forces-replacement flag reach
// the wire, in the engine's existing deterministic (type, name) sorted order
// — so output is stable across requests and a client can diff two responses
// meaningfully.
func TestPlanDiffHandler_JSON_OKCarriesPerResourceDeltasInSortedOrder(t *testing.T) {
	t.Parallel()

	h := newJSONPlanDiffHandler(okPlanOutcome())

	var first string
	for i := 0; i < 3; i++ {
		rr := servePlanDiffAccept(h, jsonPlanDiffURL, "application/json")

		var got planDiffBody
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
		}

		want := []struct {
			typ, name, kind string
			forces          bool
		}{
			{"oci_core_instance", "web", "changed", true},
			{"oci_core_vcn", "main", "added", false},
			{"oci_load_balancer", "edge", "removed", true},
		}
		if len(got.Resources) != len(want) {
			t.Fatalf("resources has %d entries, want %d; body: %s", len(got.Resources), len(want), rr.Body.String())
		}
		for j, w := range want {
			r := got.Resources[j]
			if r.Type != w.typ || r.Name != w.name {
				t.Errorf("resources[%d] = %s.%s, want %s.%s (engine's sorted order must be preserved)", j, r.Type, r.Name, w.typ, w.name)
			}
			if r.Kind != w.kind {
				t.Errorf("resources[%d].kind = %q, want %q", j, r.Kind, w.kind)
			}
			if r.ForcesReplacement != w.forces {
				t.Errorf("resources[%d].forcesReplacement = %v, want %v", j, r.ForcesReplacement, w.forces)
			}
		}

		// Stable across requests, byte-for-byte.
		if i == 0 {
			first = rr.Body.String()
		} else if rr.Body.String() != first {
			t.Errorf("response %d differs from the first for identical input\n got: %s\nwant: %s", i, rr.Body.String(), first)
		}
	}
}

// TestPlanDiffHandler_JSON_NonOKKindsCarryKindAndNothingElse pins the
// contract that makes the JSON safe to expose: a non-ok Outcome round-trips as
// its Kind alone — no HCL-parser internals, no git internals, no error
// strings. The cause stays server-side in the logs.
//
// This endpoint has no `unavailable` analogue: a Terraform resource block is
// always statically resolvable from the materialized subtree, so there is no
// registry-pull case to decline.
func TestPlanDiffHandler_JSON_NonOKKindsCarryKindAndNothingElse(t *testing.T) {
	t.Parallel()

	nonOK := []plandiff.Kind{
		plandiff.NoPriorVersion,
		plandiff.CouldNotRender,
		plandiff.ExceededLimits,
	}

	for _, kind := range nonOK {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			// Fully-populated payload alongside a non-ok Kind — a shape the
			// real engine never produces — so the test fails loudly if the
			// projection ever emits data based on anything other than Kind.
			leaky := okPlanOutcome()
			leaky.Kind = kind
			leaky.Diff.Unified = "hcl: parse error at /var/lib/work/9f3/prod/main.tf:42"

			rr := servePlanDiffAccept(newJSONPlanDiffHandler(leaky), jsonPlanDiffURL, "application/json")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
			}

			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
			}
			if body["kind"] != string(kind) {
				t.Errorf("kind = %v, want %q", body["kind"], kind)
			}
			if len(body) != 1 {
				t.Errorf("non-ok body carries %d keys, want exactly 1 (kind); body: %s", len(body), rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "hcl") || strings.Contains(rr.Body.String(), "/var/lib") {
				t.Errorf("non-ok body leaked engine internals: %s", rr.Body.String())
			}
		})
	}
}

// TestPlanDiffHandler_NonJSONAcceptReturnsHTML is the live-UI regression
// guard — see the chart-diff twin for the full rationale. timeline.js fetches
// this endpoint without an Accept header, so the browser sends "*/*", which
// must keep yielding today's HTML fragment byte-for-byte.
func TestPlanDiffHandler_NonJSONAcceptReturnsHTML(t *testing.T) {
	t.Parallel()

	h := newJSONPlanDiffHandler(okPlanOutcome())

	baseline := servePlanDiffAccept(h, jsonPlanDiffURL, "")
	if baseline.Code != http.StatusOK {
		t.Fatalf("baseline status = %d, want 200; body: %s", baseline.Code, baseline.Body.String())
	}

	cases := []struct {
		name   string
		accept string
	}{
		{"absent header", ""},
		{"wildcard from XMLHttpRequest", "*/*"},
		{"html", "text/html"},
		{"html with wildcard", "text/html,application/xhtml+xml,*/*;q=0.8"},
		{"a type-level wildcard is not an explicit mention", "application/*"},
		{"an unrelated type", "application/xml"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := servePlanDiffAccept(h, jsonPlanDiffURL, tt.accept)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html...", ct)
			}
			if rr.Body.String() != baseline.Body.String() {
				t.Errorf("Accept %q changed the HTML body\n got: %s\nwant: %s", tt.accept, rr.Body.String(), baseline.Body.String())
			}
			if strings.HasPrefix(strings.TrimSpace(rr.Body.String()), "{") {
				t.Errorf("Accept %q returned what looks like JSON: %s", tt.accept, rr.Body.String())
			}
		})
	}
}

// TestPlanDiffHandler_JSON_MissingParamIsJSONObject verifies a 400 in the JSON
// representation is itself a JSON object carrying a generic message that never
// names which parameter was missing.
func TestPlanDiffHandler_JSON_MissingParamIsJSONObject(t *testing.T) {
	t.Parallel()

	h := web.NewPlanDiffHandler(&fakePlanDiffEngine{}, &fakePlanRepoResolver{}, &fakeChangesetExistenceChecker{})

	urls := []string{
		"/api/changesets/detail/plan-diff?commitSha=sha&path=prod",
		"/api/changesets/detail/plan-diff?repo=r&path=prod",
		"/api/changesets/detail/plan-diff?repo=r&commitSha=sha",
		"/api/changesets/detail/plan-diff",
	}

	for _, url := range urls {
		t.Run(url, func(t *testing.T) {
			t.Parallel()

			rr := servePlanDiffAccept(h, url, "application/json")
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("400 body is not valid JSON: %v; body: %s", err, rr.Body.String())
			}
			msg, ok := body["error"].(string)
			if !ok || msg == "" {
				t.Errorf("400 body has no non-empty error string; body: %s", rr.Body.String())
			}
			for _, param := range []string{"repo", "commitSha", "path"} {
				if strings.Contains(msg, param) {
					t.Errorf("error message %q names the missing parameter %q; want a generic message", msg, param)
				}
			}
		})
	}
}

// TestPlanDiffHandler_JSON_UnknownChangesetAndWrongPath_404Indistinguishable
// carries the non-enumeration property into the JSON representation: the
// gates run before any representation is chosen, so switching Accept headers
// must not become a way to tell an unknown commit from a wrong path.
func TestPlanDiffHandler_JSON_UnknownChangesetAndWrongPath_404Indistinguishable(t *testing.T) {
	t.Parallel()

	engineFn := func(context.Context, plandiff.PlanRepo, plandiff.Request) plandiff.Outcome {
		return plandiff.Outcome{Kind: plandiff.OK}
	}
	newHandler := func(checker *fakeChangesetExistenceChecker) *web.PlanDiffHandler {
		return web.NewPlanDiffHandler(
			&fakePlanDiffEngine{fn: engineFn},
			&fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return stubPlanRepo{}, nil }},
			checker,
		)
	}

	unknownCommit := newHandler(&fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return changeset.Changeset{}, false, nil
	}})
	knownCommitWrongPath := newHandler(&fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return changeset.Changeset{}, true, nil // found, but no Changes at all -> no path match
	}})

	for _, accept := range []string{"application/json", ""} {
		name := accept
		if name == "" {
			name = "html"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			unknownRR := servePlanDiffAccept(unknownCommit, jsonPlanDiffURL, accept)
			wrongPathRR := servePlanDiffAccept(knownCommitWrongPath, jsonPlanDiffURL, accept)

			if unknownRR.Code != http.StatusNotFound {
				t.Fatalf("unknown changeset status = %d, want 404; body: %s", unknownRR.Code, unknownRR.Body.String())
			}
			if unknownRR.Code != wrongPathRR.Code {
				t.Fatalf("status codes differ: unknown = %d, wrong path = %d", unknownRR.Code, wrongPathRR.Code)
			}
			if unknownRR.Body.String() != wrongPathRR.Body.String() {
				t.Errorf("bodies differ: unknown = %q, wrong path = %q; want indistinguishable", unknownRR.Body.String(), wrongPathRR.Body.String())
			}
			for k := range unknownRR.Header() {
				if unknownRR.Header().Get(k) != wrongPathRR.Header().Get(k) {
					t.Errorf("header %s differs: unknown = %q, wrong path = %q", k, unknownRR.Header().Get(k), wrongPathRR.Header().Get(k))
				}
			}
		})
	}
}

// TestPlanDiffHandler_JSON_404IsJSONObject verifies the 404 honors the
// negotiated representation, and its HTML twin stays http.NotFound's exact
// body so non-JSON callers see no change.
func TestPlanDiffHandler_JSON_404IsJSONObject(t *testing.T) {
	t.Parallel()

	h := web.NewPlanDiffHandler(
		&fakePlanDiffEngine{fn: func(context.Context, plandiff.PlanRepo, plandiff.Request) plandiff.Outcome {
			return plandiff.Outcome{Kind: plandiff.OK}
		}},
		&fakePlanRepoResolver{fn: func(string) (plandiff.PlanRepo, error) { return stubPlanRepo{}, nil }},
		&fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
			return changeset.Changeset{}, false, nil
		}},
	)

	rr := servePlanDiffAccept(h, jsonPlanDiffURL, "application/json")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 body is not valid JSON: %v; body: %s", err, rr.Body.String())
	}
	if msg, ok := body["error"].(string); !ok || msg == "" {
		t.Errorf("404 body has no non-empty error string; body: %s", rr.Body.String())
	}

	htmlRR := servePlanDiffAccept(h, jsonPlanDiffURL, "*/*")
	if got := htmlRR.Body.String(); got != "404 page not found\n" {
		t.Errorf("HTML 404 body = %q, want http.NotFound's %q", got, "404 page not found\n")
	}
}

// TestPlanDiffHandler_SecurityHeadersSetOnEveryRepresentation verifies the
// security headers are set regardless of representation or status.
func TestPlanDiffHandler_SecurityHeadersSetOnEveryRepresentation(t *testing.T) {
	t.Parallel()

	ok := newJSONPlanDiffHandler(okPlanOutcome())
	badRequest := web.NewPlanDiffHandler(&fakePlanDiffEngine{}, &fakePlanRepoResolver{}, &fakeChangesetExistenceChecker{})

	cases := []struct {
		name   string
		h      http.Handler
		url    string
		accept string
	}{
		{"ok json", ok, jsonPlanDiffURL, "application/json"},
		{"ok html", ok, jsonPlanDiffURL, "*/*"},
		{"400 json", badRequest, "/api/changesets/detail/plan-diff", "application/json"},
		{"400 html", badRequest, "/api/changesets/detail/plan-diff", "*/*"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := servePlanDiffAccept(tt.h, tt.url, tt.accept)
			if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}
