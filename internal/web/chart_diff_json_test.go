package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// serveChartDiffAccept issues a GET against a chart-diff handler with the
// given Accept header. An empty accept sends no Accept header at all — the
// distinction matters, since an absent header and "*/*" are different
// requests even though both must yield HTML.
func serveChartDiffAccept(h http.Handler, url, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestChartDiffHandler_JSON_OKCarriesDiffTextAndSummary is the tracer bullet:
// an ok Outcome negotiated as JSON carries the unified diff text, its summary
// counts, and the truncation flag, so a client can present either the counts
// or the full diff depending on how much room it has.
func TestChartDiffHandler_JSON_OKCarriesDiffTextAndSummary(t *testing.T) {
	t.Parallel()

	h := newChartDiffHandlerForOutcome(chartdiff.Outcome{
		Kind: chartdiff.OK,
		Diff: manifestdiff.Result{
			Unified: "-image: 1.9.0\n+image: 2.0.0\n",
			Summary: manifestdiff.Summary{
				ManifestsChanged: 12,
				LinesAdded:       200,
				LinesRemoved:     140,
			},
			Truncated: false,
		},
	})

	rr := serveChartDiffAccept(h, defaultChartDiffURL, "application/json")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got struct {
		Kind string `json:"kind"`
		Diff struct {
			Unified   string `json:"unified"`
			Truncated bool   `json:"truncated"`
			Summary   struct {
				ManifestsChanged int `json:"manifestsChanged"`
				LinesAdded       int `json:"linesAdded"`
				LinesRemoved     int `json:"linesRemoved"`
			} `json:"summary"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
	}

	if got.Kind != "ok" {
		t.Errorf("kind = %q, want ok", got.Kind)
	}
	if got.Diff.Unified != "-image: 1.9.0\n+image: 2.0.0\n" {
		t.Errorf("diff.unified = %q, want the engine's unified text", got.Diff.Unified)
	}
	if got.Diff.Truncated {
		t.Error("diff.truncated = true, want false for an untruncated diff")
	}
	if got.Diff.Summary.ManifestsChanged != 12 {
		t.Errorf("diff.summary.manifestsChanged = %d, want 12", got.Diff.Summary.ManifestsChanged)
	}
	if got.Diff.Summary.LinesAdded != 200 {
		t.Errorf("diff.summary.linesAdded = %d, want 200", got.Diff.Summary.LinesAdded)
	}
	if got.Diff.Summary.LinesRemoved != 140 {
		t.Errorf("diff.summary.linesRemoved = %d, want 140", got.Diff.Summary.LinesRemoved)
	}
}

// TestChartDiffHandler_JSON_NonOKKindsCarryKindAndNothingElse pins the
// contract that makes the JSON safe to expose: a non-ok Outcome round-trips
// to the wire as its Kind alone. The Outcome type deliberately carries no
// internal error detail, and the wire shape must not reintroduce any — no
// error strings, no Helm output, no git internals. The cause stays
// server-side in the logs.
//
// The engine is fed a fully-populated Diff alongside each non-ok Kind — a
// shape the real engine never produces — so the test fails loudly if the
// projection ever starts emitting diff data based on anything other than Kind.
func TestChartDiffHandler_JSON_NonOKKindsCarryKindAndNothingElse(t *testing.T) {
	t.Parallel()

	nonOK := []chartdiff.Kind{
		chartdiff.NoPriorVersion,
		chartdiff.Unavailable,
		chartdiff.CouldNotRender,
		chartdiff.ExceededLimits,
	}

	for _, kind := range nonOK {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			h := newChartDiffHandlerForOutcome(chartdiff.Outcome{
				Kind: kind,
				Diff: manifestdiff.Result{
					Unified:   "helm: error rendering /tmp/build-9f3/templates/deploy.yaml",
					Summary:   manifestdiff.Summary{ManifestsChanged: 7, LinesAdded: 5, LinesRemoved: 3},
					Truncated: true,
				},
			})

			rr := serveChartDiffAccept(h, defaultChartDiffURL, "application/json")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
			}

			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
			}

			// The kind is the existing vocabulary verbatim.
			if body["kind"] != string(kind) {
				t.Errorf("kind = %v, want %q", body["kind"], kind)
			}
			// ...and it is the ONLY key. Anything else is a leak.
			if len(body) != 1 {
				t.Errorf("non-ok body carries %d keys, want exactly 1 (kind); body: %s", len(body), rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "helm") || strings.Contains(rr.Body.String(), "/tmp/") {
				t.Errorf("non-ok body leaked engine internals: %s", rr.Body.String())
			}
		})
	}
}

// TestChartDiffHandler_JSON_TruncatedDiffIsIdentifiableAsTruncated verifies a
// client can tell a size-ceiling-truncated diff from a complete one using the
// JSON alone, so it never presents a partial diff as the whole blast radius.
// The summary counts remain the true pre-truncation totals.
func TestChartDiffHandler_JSON_TruncatedDiffIsIdentifiableAsTruncated(t *testing.T) {
	t.Parallel()

	h := newChartDiffHandlerForOutcome(chartdiff.Outcome{
		Kind: chartdiff.OK,
		Diff: manifestdiff.Result{
			Unified:   "-image: 1.9.0\n",
			Summary:   manifestdiff.Summary{ManifestsChanged: 40, LinesAdded: 900, LinesRemoved: 850},
			Truncated: true,
		},
	})

	rr := serveChartDiffAccept(h, defaultChartDiffURL, "application/json")

	var got struct {
		Diff struct {
			Truncated bool `json:"truncated"`
			Summary   struct {
				LinesAdded int `json:"linesAdded"`
			} `json:"summary"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
	}

	if !got.Diff.Truncated {
		t.Errorf("diff.truncated = false for a truncated diff; body: %s", rr.Body.String())
	}
	// The summary is the TRUE total, not a count of the truncated text.
	if got.Diff.Summary.LinesAdded != 900 {
		t.Errorf("diff.summary.linesAdded = %d, want the true pre-truncation total 900", got.Diff.Summary.LinesAdded)
	}
}

// TestChartDiffHandler_NonJSONAcceptReturnsHTML is the live-UI regression
// guard. The dashboard's own timeline.js fetches this endpoint with
// XMLHttpRequest and never sets an Accept header, so the browser sends "*/*".
// Treating a wildcard as opting into JSON would silently replace the HTML
// fragment the UI splices into the chart-change detail slot with a JSON
// document. Each case below must return today's HTML, byte-for-byte identical
// to what the handler returned before negotiation existed.
func TestChartDiffHandler_NonJSONAcceptReturnsHTML(t *testing.T) {
	t.Parallel()

	h := newChartDiffHandlerForOutcome(chartdiff.Outcome{
		Kind: chartdiff.OK,
		Diff: manifestdiff.Result{
			Unified: "-image: 1.9.0\n+image: 2.0.0\n",
			Summary: manifestdiff.Summary{ManifestsChanged: 1, LinesAdded: 1, LinesRemoved: 1},
		},
	})

	// The baseline: no Accept header at all, which is what the handler saw
	// before negotiation existed.
	baseline := serveChartDiffAccept(h, defaultChartDiffURL, "")
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

			rr := serveChartDiffAccept(h, defaultChartDiffURL, tt.accept)
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

// TestChartDiffHandler_JSON_MissingParamIsJSONObject verifies a 400 in the
// JSON representation is itself a JSON object carrying a generic message, so
// a client parses every response — success or failure — with one code path
// and never has to sniff whether a body is JSON or plain text.
func TestChartDiffHandler_JSON_MissingParamIsJSONObject(t *testing.T) {
	t.Parallel()

	h := web.NewChartDiffHandler(&spyChartDiffEngine{}, &spyRepoResolver{}, &fakeChangesetExistenceChecker{})

	urls := []string{
		"/api/changesets/detail/chart-diff?commitSha=sha&path=tenant",
		"/api/changesets/detail/chart-diff?repo=r&path=tenant",
		"/api/changesets/detail/chart-diff?repo=r&commitSha=sha",
		"/api/changesets/detail/chart-diff",
	}

	for _, url := range urls {
		t.Run(url, func(t *testing.T) {
			t.Parallel()

			rr := serveChartDiffAccept(h, url, "application/json")
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
			// The message must be generic — it must not name which parameter
			// was missing, and must never echo caller input.
			for _, param := range []string{"repo", "commitSha", "path"} {
				if strings.Contains(msg, param) {
					t.Errorf("error message %q names the missing parameter %q; want a generic message", msg, param)
				}
			}
		})
	}
}

// TestChartDiffHandler_JSON_UnknownChangesetAndWrongPath_404Indistinguishable
// carries the endpoint's non-enumeration property into the JSON
// representation. The existence and tenant-path gates run before any
// representation is chosen, so "this commit was never ingested" and "this
// commit exists but you asked for the wrong path" must stay byte-for-byte
// indistinguishable in JSON exactly as they already are in HTML — and
// switching representations must not become a way to tell them apart.
func TestChartDiffHandler_JSON_UnknownChangesetAndWrongPath_404Indistinguishable(t *testing.T) {
	t.Parallel()

	unknownCommit := web.NewChartDiffHandler(&spyChartDiffEngine{}, &spyRepoResolver{}, neverFoundChecker())

	wrongPathChecker := &fakeChangesetExistenceChecker{fn: func(string, string) (changeset.Changeset, bool, error) {
		return changeset.Changeset{}, true, nil // found, but no Changes at all -> no path match
	}}
	knownCommitWrongPath := web.NewChartDiffHandler(&spyChartDiffEngine{}, &spyRepoResolver{}, wrongPathChecker)

	for _, accept := range []string{"application/json", ""} {
		name := accept
		if name == "" {
			name = "html"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			unknownRR := serveChartDiffAccept(unknownCommit, defaultChartDiffURL, accept)
			wrongPathRR := serveChartDiffAccept(knownCommitWrongPath, defaultChartDiffURL, accept)

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

// TestChartDiffHandler_JSON_404IsJSONObject verifies the 404 honors the
// negotiated representation rather than falling back to http.NotFound's plain
// text, so a JSON client's parse path is uniform across every status.
func TestChartDiffHandler_JSON_404IsJSONObject(t *testing.T) {
	t.Parallel()

	h := web.NewChartDiffHandler(&spyChartDiffEngine{}, &spyRepoResolver{}, neverFoundChecker())

	rr := serveChartDiffAccept(h, defaultChartDiffURL, "application/json")
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
}

// TestChartDiffHandler_ErrorPathsKeepHTMLBodiesByteIdentical guards the other
// half of the error contract: adding a JSON error representation must not
// disturb what a non-JSON caller sees. The HTML 404 stays http.NotFound's
// exact body — not the JSON message rendered as text — and the 400 stays
// http.Error's.
func TestChartDiffHandler_ErrorPathsKeepHTMLBodiesByteIdentical(t *testing.T) {
	t.Parallel()

	notFound := web.NewChartDiffHandler(&spyChartDiffEngine{}, &spyRepoResolver{}, neverFoundChecker())
	rr := serveChartDiffAccept(notFound, defaultChartDiffURL, "*/*")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if got := rr.Body.String(); got != "404 page not found\n" {
		t.Errorf("HTML 404 body = %q, want http.NotFound's %q", got, "404 page not found\n")
	}

	badRequest := web.NewChartDiffHandler(&spyChartDiffEngine{}, &spyRepoResolver{}, &fakeChangesetExistenceChecker{})
	rr = serveChartDiffAccept(badRequest, "/api/changesets/detail/chart-diff", "*/*")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if strings.HasPrefix(strings.TrimSpace(rr.Body.String()), "{") {
		t.Errorf("HTML 400 body looks like JSON: %s", rr.Body.String())
	}
}

// TestChartDiffHandler_SecurityHeadersSetOnEveryRepresentation verifies the
// security headers are set regardless of representation or status — they are
// applied before any code path can return.
func TestChartDiffHandler_SecurityHeadersSetOnEveryRepresentation(t *testing.T) {
	t.Parallel()

	ok := newChartDiffHandlerForOutcome(chartdiff.Outcome{Kind: chartdiff.OK})
	notFound := web.NewChartDiffHandler(&spyChartDiffEngine{}, &spyRepoResolver{}, neverFoundChecker())
	badRequest := web.NewChartDiffHandler(&spyChartDiffEngine{}, &spyRepoResolver{}, &fakeChangesetExistenceChecker{})

	cases := []struct {
		name   string
		h      http.Handler
		url    string
		accept string
	}{
		{"ok json", ok, defaultChartDiffURL, "application/json"},
		{"ok html", ok, defaultChartDiffURL, "*/*"},
		{"404 json", notFound, defaultChartDiffURL, "application/json"},
		{"404 html", notFound, defaultChartDiffURL, "*/*"},
		{"400 json", badRequest, "/api/changesets/detail/chart-diff", "application/json"},
		{"400 html", badRequest, "/api/changesets/detail/chart-diff", "*/*"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rr := serveChartDiffAccept(tt.h, tt.url, tt.accept)
			if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

// TestChartDiffHandler_ExplicitJSONAcceptVariants verifies the negotiation
// rule's positive half across the header spellings a real client sends.
func TestChartDiffHandler_ExplicitJSONAcceptVariants(t *testing.T) {
	t.Parallel()

	h := newChartDiffHandlerForOutcome(chartdiff.Outcome{Kind: chartdiff.OK})

	accepts := []string{
		"application/json",
		"application/json, text/html",
		"text/html, application/json",
		"application/json;q=0.9",
		"application/json, */*",
		"Application/JSON",
	}

	for _, accept := range accepts {
		t.Run(accept, func(t *testing.T) {
			t.Parallel()

			rr := serveChartDiffAccept(h, defaultChartDiffURL, accept)
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not valid JSON: %v; body: %s", err, rr.Body.String())
			}
			if body["kind"] != "ok" {
				t.Errorf("kind = %v, want ok", body["kind"])
			}
		})
	}
}
