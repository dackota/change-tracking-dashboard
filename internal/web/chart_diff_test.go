package web_test

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/quick"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/manifestdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// fakeChartDiffEngine is a web.ChartDiffEngine test double: Diff delegates to
// a caller-supplied func, so each test configures exactly the Outcome it
// needs without a real Helm render.
type fakeChartDiffEngine struct {
	fn func(ctx context.Context, repo chartdiff.ChartRepo, req chartdiff.Request) chartdiff.Outcome
}

func (f *fakeChartDiffEngine) Diff(ctx context.Context, repo chartdiff.ChartRepo, req chartdiff.Request) chartdiff.Outcome {
	return f.fn(ctx, repo, req)
}

// fakeChartRepoResolver is a web.ChartRepoResolver test double.
type fakeChartRepoResolver struct {
	fn func(repo string) (chartdiff.ChartRepo, error)
}

func (f *fakeChartRepoResolver) ResolveChartRepo(repo string) (chartdiff.ChartRepo, error) {
	return f.fn(repo)
}

// stubChartRepo is a minimal chartdiff.ChartRepo used where the resolver
// must succeed but the fake engine never actually calls its methods.
type stubChartRepo struct{}

func (stubChartRepo) FirstParent(string) (string, error) { return "", nil }
func (stubChartRepo) MaterializeSubtreeBounded(string, string, string, gitsource.MaterializeBounds) error {
	return nil
}

// fakeChangesetExistenceChecker is a web.ChangesetExistenceChecker test
// double: GetChangeset delegates to a caller-supplied func. The zero value
// (fn nil) panics if invoked — used deliberately in tests that must prove the
// checker is never consulted for a given code path.
type fakeChangesetExistenceChecker struct {
	fn func(repo, commitSha string) (changeset.Changeset, bool, error)
}

func (f *fakeChangesetExistenceChecker) GetChangeset(repo, commitSha string) (changeset.Changeset, bool, error) {
	return f.fn(repo, commitSha)
}

// alwaysFoundChecker is a fakeChangesetExistenceChecker that reports every
// (repo, commitSha) pair as an already-ingested Changeset — the common shape
// for tests that only care about resolver/engine/rendering behavior, not
// about the existence gate itself.
func alwaysFoundChecker() *fakeChangesetExistenceChecker {
	return &fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return changeset.Changeset{}, true, nil
	}}
}

// neverFoundChecker is a fakeChangesetExistenceChecker that reports every
// (repo, commitSha) pair as never ingested — the shape the existence-gate
// tests use to assert the resolver/engine are never reached.
func neverFoundChecker() *fakeChangesetExistenceChecker {
	return &fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return changeset.Changeset{}, false, nil
	}}
}

// spyChartRepoResolver is a web.ChartRepoResolver test double that records
// whether it was ever invoked — used to prove the existence gate short-
// circuits before ResolveChartRepo runs, not just that the HTTP response
// looks right.
type spyChartRepoResolver struct {
	called bool
}

func (s *spyChartRepoResolver) ResolveChartRepo(string) (chartdiff.ChartRepo, error) {
	s.called = true
	return stubChartRepo{}, nil
}

// spyChartDiffEngine is a web.ChartDiffEngine test double that records
// whether it was ever invoked, for the same reason as spyChartRepoResolver.
type spyChartDiffEngine struct {
	called bool
}

func (s *spyChartDiffEngine) Diff(context.Context, chartdiff.ChartRepo, chartdiff.Request) chartdiff.Outcome {
	s.called = true
	return chartdiff.Outcome{Kind: chartdiff.OK}
}

// defaultChartDiffURL is the well-formed query string every test below
// reuses unless it's specifically exercising request validation.
const defaultChartDiffURL = "/api/changesets/detail/chart-diff?repo=r&commitSha=sha&path=tenant"

// newChartDiffHandlerForOutcome builds a ChartDiffHandler whose engine always
// returns outcome, backed by a resolver that always succeeds with a stub
// repo — the common shape for every test below that only cares about how one
// Outcome renders, not about request validation or resolver failure.
func newChartDiffHandlerForOutcome(outcome chartdiff.Outcome) *web.ChartDiffHandler {
	engine := &fakeChartDiffEngine{fn: func(context.Context, chartdiff.ChartRepo, chartdiff.Request) chartdiff.Outcome { return outcome }}
	resolver := &fakeChartRepoResolver{fn: func(string) (chartdiff.ChartRepo, error) { return stubChartRepo{}, nil }}
	return web.NewChartDiffHandler(engine, resolver, alwaysFoundChecker())
}

// serveChartDiff issues a GET to url against h and returns the recorded
// response.
func serveChartDiff(h http.Handler, url string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestChartDiffHandler_MissingRequiredParam_Returns400Generic verifies that
// omitting any of the three required query params (repo, commitSha, path) is
// rejected as a malformed request (400) with the shared generic body, never
// echoing caller input — mirroring changeset_detail.go's own convention. The
// zero-value checker/resolver/engine fakes (nil fn) would panic if invoked,
// proving validation short-circuits before the existence gate even runs.
func TestChartDiffHandler_MissingRequiredParam_Returns400Generic(t *testing.T) {
	t.Parallel()

	engine := &fakeChartDiffEngine{}
	resolver := &fakeChartRepoResolver{}
	checker := &fakeChangesetExistenceChecker{}
	h := web.NewChartDiffHandler(engine, resolver, checker)

	cases := []struct {
		name string
		url  string
	}{
		{"missing repo", "/api/changesets/detail/chart-diff?commitSha=sha&path=tenant"},
		{"missing commitSha", "/api/changesets/detail/chart-diff?repo=r&path=tenant"},
		{"missing path", "/api/changesets/detail/chart-diff?repo=r&commitSha=sha"},
		{"missing all", "/api/changesets/detail/chart-diff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := serveChartDiff(h, tc.url)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
			body := strings.TrimSpace(rr.Body.String())
			if body != "bad request" {
				t.Errorf("body = %q, want generic %q", body, "bad request")
			}
		})
	}
}

// TestChartDiffHandler_ResolverFailure_Returns500GenericWithoutLeakingDetail
// verifies that a ChartRepoResolver failure (e.g. a clone error) surfaces as
// a generic 500 — the underlying error text (which could embed a local
// filesystem path, a clone URL, or another internal detail) must never reach
// the response body.
func TestChartDiffHandler_ResolverFailure_Returns500GenericWithoutLeakingDetail(t *testing.T) {
	t.Parallel()

	engine := &fakeChartDiffEngine{}
	sentinel := errors.New("clone failed: /var/secret/internal/path unreachable")
	resolver := &fakeChartRepoResolver{fn: func(string) (chartdiff.ChartRepo, error) { return nil, sentinel }}
	h := web.NewChartDiffHandler(engine, resolver, alwaysFoundChecker())

	rr := serveChartDiff(h, defaultChartDiffURL)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "internal server error") {
		t.Errorf("body = %q, want generic 'internal server error'", body)
	}
	if strings.Contains(body, "/var/secret") || strings.Contains(body, "clone failed") {
		t.Errorf("error body leaks internal detail: %q", body)
	}
}

// TestChartDiffHandler_ExistenceCheckError_Returns500GenericWithoutLeakingDetail
// verifies that a ChangesetExistenceChecker failure (e.g. a store error)
// surfaces as a generic 500 without leaking the underlying error text, and —
// critically — never falls through to ResolveChartRepo/Diff on a checker
// error. A checker error means we could not confirm trust, so it must fail
// closed, not open.
func TestChartDiffHandler_ExistenceCheckError_Returns500GenericWithoutLeakingDetailAndNeverReachesResolver(t *testing.T) {
	t.Parallel()

	resolver := &spyChartRepoResolver{}
	engine := &spyChartDiffEngine{}
	sentinel := errors.New("store: query changeset: disk I/O error at /var/lib/db/changes.db")
	checker := &fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return changeset.Changeset{}, false, sentinel
	}}
	h := web.NewChartDiffHandler(engine, resolver, checker)

	rr := serveChartDiff(h, defaultChartDiffURL)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "internal server error") {
		t.Errorf("body = %q, want generic 'internal server error'", body)
	}
	if strings.Contains(body, "/var/lib/db") || strings.Contains(body, "disk I/O error") {
		t.Errorf("error body leaks internal detail: %q", body)
	}
	if resolver.called {
		t.Error("ResolveChartRepo was called despite a checker error — must fail closed")
	}
	if engine.called {
		t.Error("engine.Diff was called despite a checker error — must fail closed")
	}
}

// TestChartDiffHandler_UnknownChangeset_RejectsWithoutReachingResolverOrEngine
// is the core regression test for the CRITICAL finding: repo/commitSha arrive
// on an unauthenticated request, so ChartRepoResolver (and, behind it,
// cmd/dashboard's sourceCache — git clone/fetch of an arbitrary URL, GitHub
// App token attachment, or PlainOpen of an arbitrary local path) must never
// run for a (repo, commitSha) pair that isn't a real, already-ingested
// Changeset. Each case below is a plausible attack shape: an attacker-
// controlled clone target (SSRF/credential exfiltration), a local filesystem
// path (arbitrary-file/repo disclosure), and a degenerate/empty-ish value.
// The assertion that matters is that the resolver/engine spies are NEVER
// invoked — not merely that the HTTP response looks like a rejection.
func TestChartDiffHandler_UnknownChangeset_RejectsWithoutReachingResolverOrEngine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		repo      string
		commitSha string
	}{
		{"attacker-controlled HTTPS clone target", "https://attacker.example.com/evil.git", "deadbeef"},
		{"local filesystem path (e.g. /etc/passwd)", "/etc/passwd", "deadbeef"},
		{"another checked-out repo's local path", "/var/dashboard/repos/other-tenant", "deadbeef"},
		{"scp-like ssh URL", "git@attacker.example.com:evil/repo.git", "deadbeef"},
		{"path traversal", "../../../../etc/shadow", "deadbeef"},
		{"file scheme URL", "file:///etc/hosts", "deadbeef"},
		{"whitespace-only repo", " ", "deadbeef"},
		{"looks legitimate but never ingested", "tenant-repo", "0000000000000000000000000000000000000000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &spyChartRepoResolver{}
			engine := &spyChartDiffEngine{}
			h := web.NewChartDiffHandler(engine, resolver, neverFoundChecker())

			reqURL := "/api/changesets/detail/chart-diff?" + url.Values{
				"repo":      {tc.repo},
				"commitSha": {tc.commitSha},
				"path":      {"tenant"},
			}.Encode()

			rr := serveChartDiff(h, reqURL)

			if resolver.called {
				t.Error("ResolveChartRepo was called for a never-ingested (repo, commitSha) pair — security gate bypassed")
			}
			if engine.called {
				t.Error("engine.Diff was called for a never-ingested (repo, commitSha) pair — security gate bypassed")
			}
			if rr.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 for an unknown changeset; body: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// unknownRepoCommitPair is a generated (repo, commitSha) pair standing in for
// "any string shape an unauthenticated caller could invent" — URLs, local
// paths, ssh-like refs, traversal sequences, oversized strings, and garbage —
// recombined per call so the property test below searches a wide space
// instead of only the hand-picked cases in the table test above.
type unknownRepoCommitPair struct {
	repo      string
	commitSha string
}

var adversarialRepoFragments = []string{
	"https://attacker.example.com/evil.git",
	"/etc/passwd",
	"/var/dashboard/repos/other-tenant",
	"git@attacker.example.com:evil/repo.git",
	" ",
	"../../../../etc/shadow",
	"file:///etc/hosts",
	strings.Repeat("a", 4096),
	"tenant-repo",
	"0",
}

// Generate implements quick.Generator, picking one adversarial repo fragment
// and pairing it with a distinguishing commitSha so repeated draws of the
// same repo fragment don't collapse to identical requests.
func (unknownRepoCommitPair) Generate(rnd *rand.Rand, size int) reflect.Value {
	repo := adversarialRepoFragments[rnd.Intn(len(adversarialRepoFragments))]
	commitSha := adversarialRepoFragments[rnd.Intn(len(adversarialRepoFragments))] + "-" + strconv.Itoa(rnd.Int())
	return reflect.ValueOf(unknownRepoCommitPair{repo: repo, commitSha: commitSha})
}

// TestChartDiffHandler_NeverIngestedRepoCommitPair_NeverReachesResolverOrEngine_Property
// asserts the security invariant directly, over a generated family of
// (repo, commitSha) pairs rather than only the fixed table above: for EVERY
// pair the ChangesetExistenceChecker reports as never ingested, the
// resolver/engine must never run and the response must be a 404 — regardless
// of what shape of string an attacker chooses for repo. This subsumes the
// whole class of "attacker invents a repo string" attacks the table test
// exemplifies, catching any combination the table didn't enumerate.
func TestChartDiffHandler_NeverIngestedRepoCommitPair_NeverReachesResolverOrEngine_Property(t *testing.T) {
	t.Parallel()

	property := func(pair unknownRepoCommitPair) bool {
		resolver := &spyChartRepoResolver{}
		engine := &spyChartDiffEngine{}
		h := web.NewChartDiffHandler(engine, resolver, neverFoundChecker())

		reqURL := "/api/changesets/detail/chart-diff?" + url.Values{
			"repo":      {pair.repo},
			"commitSha": {pair.commitSha},
			"path":      {"tenant"},
		}.Encode()

		rr := serveChartDiff(h, reqURL)

		if resolver.called || engine.called {
			t.Logf("gate bypassed for repo=%q commitSha=%q", pair.repo, pair.commitSha)
			return false
		}
		return rr.Code == http.StatusNotFound
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}

// TestChartDiffHandler_OKOutcome_RendersSummaryAndUnifiedDiff verifies the
// success path: an OK Outcome renders the manifests-changed count, the
// +X/-Y line counts, and the unified diff text, all findable in the
// response body, with a data-kind="ok" marker for later client-side
// styling/detection.
func TestChartDiffHandler_OKOutcome_RendersSummaryAndUnifiedDiff(t *testing.T) {
	t.Parallel()

	outcome := chartdiff.Outcome{
		Kind: chartdiff.OK,
		Diff: manifestdiff.Result{
			Unified: "-  replicas: 1\n+  replicas: 2\n",
			Summary: manifestdiff.Summary{ManifestsChanged: 1, LinesAdded: 1, LinesRemoved: 1},
		},
	}
	h := newChartDiffHandlerForOutcome(outcome)

	rr := serveChartDiff(h, defaultChartDiffURL)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-kind="ok"`) {
		t.Errorf("body missing data-kind=\"ok\" marker; got:\n%s", body)
	}
	if !strings.Contains(body, "1 manifest") {
		t.Errorf("body missing manifests-changed count; got:\n%s", body)
	}
	if !strings.Contains(body, "+1") || !strings.Contains(body, "-1") {
		t.Errorf("body missing +/- line counts; got:\n%s", body)
	}
	if !strings.Contains(body, "replicas: 1") || !strings.Contains(body, "replicas: 2") {
		t.Errorf("body missing unified diff text; got:\n%s", body)
	}
	if strings.Contains(body, "truncated") {
		t.Errorf("body shows a truncation notice for a non-truncated diff; got:\n%s", body)
	}
}

// TestChartDiffHandler_TruncatedOKOutcome_ShowsTruncationNoticeAndTrueCounts
// verifies PRD user story 8: when Outcome.Diff.Truncated is true, the
// response shows a clear truncation notice alongside the (true, untruncated)
// summary counts.
func TestChartDiffHandler_TruncatedOKOutcome_ShowsTruncationNoticeAndTrueCounts(t *testing.T) {
	t.Parallel()

	outcome := chartdiff.Outcome{
		Kind: chartdiff.OK,
		Diff: manifestdiff.Result{
			Unified:   "-  a\n",
			Summary:   manifestdiff.Summary{ManifestsChanged: 5, LinesAdded: 500, LinesRemoved: 500},
			Truncated: true,
		},
	}
	h := newChartDiffHandlerForOutcome(outcome)

	rr := serveChartDiff(h, defaultChartDiffURL)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(strings.ToLower(body), "truncat") {
		t.Errorf("body missing a truncation notice for a truncated diff; got:\n%s", body)
	}
	if !strings.Contains(body, "5 manifest") || !strings.Contains(body, "+500") || !strings.Contains(body, "-500") {
		t.Errorf("body missing the true (untruncated) summary counts; got:\n%s", body)
	}
}

// TestChartDiffHandler_NonOKOutcomes_RenderDistinctClassifiedMessages
// verifies PRD user stories 11-13 and the ExceededLimits case: each of the
// four non-OK Outcome Kinds renders its own distinct message and data-kind
// marker, is still a 200 response (a classified non-availability is not an
// HTTP error), and — for CouldNotRender/ExceededLimits in particular — never
// echoes any internal error detail (Outcome carries none to begin with, but
// this proves the rendering path doesn't invent any).
func TestChartDiffHandler_NonOKOutcomes_RenderDistinctClassifiedMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind    chartdiff.Kind
		wantsIn []string
	}{
		{chartdiff.NoPriorVersion, []string{"no prior version"}},
		{chartdiff.Unavailable, []string{"unavailable", "not vendored", "registry-pull"}},
		{chartdiff.CouldNotRender, []string{"could not render"}},
		{chartdiff.ExceededLimits, []string{"exceeded", "limits"}},
	}

	var bodies []string
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			h := newChartDiffHandlerForOutcome(chartdiff.Outcome{Kind: tt.kind})

			rr := serveChartDiff(h, defaultChartDiffURL)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (a classified non-availability is not an HTTP error); body: %s", rr.Code, rr.Body.String())
			}
			body := rr.Body.String()
			bodies = append(bodies, body)

			if !strings.Contains(body, `data-kind="`+string(tt.kind)+`"`) {
				t.Errorf("body missing data-kind=%q marker; got:\n%s", tt.kind, body)
			}
			lower := strings.ToLower(body)
			for _, want := range tt.wantsIn {
				if !strings.Contains(lower, want) {
					t.Errorf("body missing expected substring %q; got:\n%s", want, body)
				}
			}
		})
	}

	// Every one of the four messages must be textually distinct from the
	// others — an operator seeing the rendered slot must be able to tell
	// which classification occurred, not read the same generic sentence
	// four times.
	seen := make(map[string]bool, len(bodies))
	for i, body := range bodies {
		if seen[body] {
			t.Errorf("outcome %d (%s) rendered a body identical to another kind's, want each classified message distinct", i, tests[i].kind)
		}
		seen[body] = true
	}
}

// TestChartDiffHandler_SecurityHeaders_PresentOnEveryResponse verifies the
// chart-diff endpoint carries the same conservative security headers as the
// rest of the dashboard — including the UNCHANGED Content-Security-Policy
// (PRD: "no CSP change") — on a success response, a 400, and a 500, so a
// slow or bounded-out chart render can never regress the app's baseline
// posture.
func TestChartDiffHandler_SecurityHeaders_PresentOnEveryResponse(t *testing.T) {
	t.Parallel()

	okHandler := newChartDiffHandlerForOutcome(chartdiff.Outcome{Kind: chartdiff.OK})
	failEngine := &fakeChartDiffEngine{}
	failResolver := &fakeChartRepoResolver{fn: func(string) (chartdiff.ChartRepo, error) { return nil, errors.New("boom") }}
	failHandler := web.NewChartDiffHandler(failEngine, failResolver, alwaysFoundChecker())

	cases := []struct {
		name string
		url  string
		h    *web.ChartDiffHandler
	}{
		{"200 success", defaultChartDiffURL, okHandler},
		{"400 missing param", "/api/changesets/detail/chart-diff?repo=r", okHandler},
		{"500 resolver failure", defaultChartDiffURL, failHandler},
	}

	wantHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := serveChartDiff(tc.h, tc.url)

			for k, v := range wantHeaders {
				if got := rr.Header().Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
			csp := rr.Header().Get("Content-Security-Policy")
			if csp == "" {
				t.Error("missing Content-Security-Policy header")
			}
			if extractDirective(csp, "script-src") != "script-src 'self'" {
				t.Errorf("script-src directive = %q, want unchanged \"script-src 'self'\"", extractDirective(csp, "script-src"))
			}
		})
	}
}

// adversarialManifestPayload is a randomized combination of HTML-metacharacter-
// bearing fragments (script tags, event-handler attributes, quotes,
// ampersands) interleaved with ordinary diff-like text, used to drive the
// HTML-escaping property test below. A real manifest's YAML content is
// untrusted tenant repository input — this generator stands in for "anything
// a tenant's values.yaml or template could produce," not just one
// hand-picked XSS payload.
type adversarialManifestPayload string

var adversarialFragments = []string{
	`<script>alert(1)</script>`,
	`<img src=x onerror=alert(1)>`,
	`"><svg onload=alert(1)>`,
	`& < > " '`,
	`value with & ampersand`,
	`<b>bold</b>`,
	"line one\nline two with <tag>",
	`normal-looking-value-42`,
	``,
}

// Generate implements quick.Generator, recombining 1-4 fragments per
// generated payload.
func (adversarialManifestPayload) Generate(rnd *rand.Rand, size int) reflect.Value {
	n := rnd.Intn(4) + 1
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(adversarialFragments[rnd.Intn(len(adversarialFragments))])
		b.WriteString(" ")
	}
	return reflect.ValueOf(adversarialManifestPayload(b.String()))
}

// TestChartDiffHandler_ManifestValueContainingHTML_IsEscapedNotInjected_Property
// asserts the untrusted-input structural invariant for this handler: for
// EVERY possible unified-diff payload containing an HTML metacharacter
// (&, <, or >), the raw payload must never appear verbatim in the rendered
// response body — html/template's auto-escaping must have transformed it —
// so a manifest value that happens to contain markup can never break out of
// the <pre> text node it's rendered into. This subsumes the single
// hand-picked "<script>" example changeset_detail_test.go asserts for the
// sibling value-change endpoint, generalizing it across a whole generated
// family of payloads instead of one.
func TestChartDiffHandler_ManifestValueContainingHTML_IsEscapedNotInjected_Property(t *testing.T) {
	t.Parallel()

	property := func(payload adversarialManifestPayload) bool {
		raw := string(payload)
		if !strings.ContainsAny(raw, "&<>") {
			return true // nothing for html/template to escape; vacuously holds
		}

		h := newChartDiffHandlerForOutcome(chartdiff.Outcome{
			Kind: chartdiff.OK,
			Diff: manifestdiff.Result{Unified: raw, Summary: manifestdiff.Summary{ManifestsChanged: 1}},
		})

		rr := serveChartDiff(h, defaultChartDiffURL)

		body := rr.Body.String()
		if strings.Contains(body, raw) {
			t.Logf("raw payload %q found verbatim in rendered body, want it HTML-escaped:\n%s", raw, body)
			return false
		}
		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 60}); err != nil {
		t.Error(err)
	}
}
