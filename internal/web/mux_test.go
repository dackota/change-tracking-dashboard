// mux_test.go exercises the real composed HTTP surface — the same
// web.NewMux cmd/dashboard serves. Before NewMux existed the routing table
// could only be built inside main, so no test could reach it: a route dropped
// or misspelled during a refactor would have been caught by nothing until it
// 404'd in production.
package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/config"
	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// riskAwareConfigSnapshot is a ConfigSnapshot that also supplies risk rules —
// the shape the real config watcher has, where one value fills both roles. It
// records whether its rules were ever read.
type riskAwareConfigSnapshot struct {
	fakeConfigSnapshot
	rules []changeset.RiskRule

	mu   sync.Mutex
	read bool
}

func (r *riskAwareConfigSnapshot) RiskRules() []changeset.RiskRule {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.read = true
	return r.rules
}

func (r *riskAwareConfigSnapshot) rulesRead() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read
}

// muxDeps returns a fully-populated web.Deps backed by test doubles and st.
func muxDeps(t *testing.T, st *store.Store) web.Deps {
	t.Helper()

	return web.Deps{
		Store:      st,
		PollHealth: pollstatus.New(),
		Config:     fakeConfigSnapshot{cfg: &config.Config{}},
		ChartDiff:  &fakeChartDiffEngine{},
		PlanDiff:   &fakePlanDiffEngine{},
		Repos:      &fakeRepoResolver{fn: func(string) (subtree.Repo, error) { return nil, nil }},
	}
}

// newMux builds the production mux over test doubles, failing the test if it
// cannot be built.
func newMux(t *testing.T, st *store.Store) http.Handler {
	t.Helper()

	mux, err := web.NewMux(muxDeps(t, st))
	if err != nil {
		t.Fatalf("web.NewMux: %v", err)
	}
	return mux
}

// TestNewMux_ServesEveryRoute walks the dashboard's whole advertised surface
// and asserts each route is actually registered. The assertion is deliberately
// weak on status — these requests carry no valid parameters, so a 400 is a
// perfectly good answer — and strong on one thing: nothing 404s, because a 404
// here means the route is missing from the table entirely.
func TestNewMux_ServesEveryRoute(t *testing.T) {
	t.Parallel()

	mux := newMux(t, newTestStore(t))

	routes := []struct {
		name string
		path string
	}{
		{name: "timeline", path: "/"},
		{name: "static assets", path: "/static/timeline.js"},
		{name: "changesets API", path: "/api/changesets"},
		{name: "changeset detail API", path: "/api/changesets/detail"},
		{name: "chart diff API", path: "/api/changesets/detail/chart-diff"},
		{name: "plan diff API", path: "/api/changesets/detail/plan-diff"},
		{name: "trackers page", path: "/trackers"},
		{name: "repositories page", path: "/repositories"},
		{name: "changes page", path: "/changes"},
		{name: "healthz", path: "/healthz"},
	}

	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rr.Code == http.StatusNotFound {
				t.Errorf("GET %s = 404 — the route is not registered on the production mux", tc.path)
			}
		})
	}
}

// TestNewMux_HealthzIsServedAtTheRouteMainQuiets pins the coupling that used
// to be maintained by hand across two files: the pattern main passes to
// telemetry.WithQuietRoutes must be the pattern the mux actually registers, or
// the probe's log line stops being suppressed and nobody notices.
func TestNewMux_HealthzIsServedAtTheRouteMainQuiets(t *testing.T) {
	t.Parallel()

	method, path, ok := strings.Cut(web.HealthzRoute, " ")
	if !ok {
		t.Fatalf("HealthzRoute = %q, want a %q-style method+path pattern", web.HealthzRoute, "GET /healthz")
	}

	mux := newMux(t, newTestStore(t))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(method, path, nil))

	if rr.Code != http.StatusOK {
		t.Errorf("%s = %d, want 200 — HealthzRoute does not name a route this mux serves", web.HealthzRoute, rr.Code)
	}
}

// TestNewMux_UnmatchedPathsFallThroughToTheTimeline documents the shape of
// the route table rather than wishing for a different one: "/" is registered
// as a catch-all, so any path no other route claims — and any /healthz request
// whose method is not GET — renders the timeline with 200 rather than 404ing.
//
// This is long-standing behavior, unchanged by extracting the mux. It is
// pinned here because it was previously unobservable from any test, and
// because it is the kind of thing worth deciding deliberately: a typo'd
// dashboard URL currently looks like a working page.
func TestNewMux_UnmatchedPathsFallThroughToTheTimeline(t *testing.T) {
	t.Parallel()

	mux := newMux(t, newTestStore(t))

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "unknown path", method: http.MethodGet, path: "/no/such/page"},
		{name: "healthz with a non-GET method", method: http.MethodPost, path: "/healthz"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))

			if rr.Code != http.StatusOK {
				t.Errorf("%s %s = %d, want 200 (served by the \"/\" catch-all)", tc.method, tc.path, rr.Code)
			}
			if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("%s %s Content-Type = %q, want HTML — expected the timeline handler to have served this",
					tc.method, tc.path, ct)
			}
		})
	}
}

// TestNewMux_MissingDependency_IsReportedNotDeferred verifies NewMux refuses
// to build an incomplete mux. The failure mode this prevents is a nil
// collaborator that only surfaces as a panic on the first request to whichever
// route needed it — potentially long after startup, on a rarely-hit endpoint.
func TestNewMux_MissingDependency_IsReportedNotDeferred(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	tests := []struct {
		name    string
		mutate  func(*web.Deps)
		wantErr string
	}{
		{name: "store", mutate: func(d *web.Deps) { d.Store = nil }, wantErr: "Store"},
		{name: "poll health", mutate: func(d *web.Deps) { d.PollHealth = nil }, wantErr: "PollHealth"},
		{name: "config", mutate: func(d *web.Deps) { d.Config = nil }, wantErr: "Config"},
		{name: "chart diff engine", mutate: func(d *web.Deps) { d.ChartDiff = nil }, wantErr: "ChartDiff"},
		{name: "plan diff engine", mutate: func(d *web.Deps) { d.PlanDiff = nil }, wantErr: "PlanDiff"},
		{name: "repo resolver", mutate: func(d *web.Deps) { d.Repos = nil }, wantErr: "Repos"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			deps := muxDeps(t, st)
			tc.mutate(&deps)

			mux, err := web.NewMux(deps)
			if err == nil {
				t.Fatalf("NewMux with no %s = nil error, want a rejection", tc.name)
			}
			if mux != nil {
				t.Error("NewMux returned a handler alongside an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not name the missing dependency %q", err, tc.wantErr)
			}
		})
	}
}

// TestNewMux_ConfigSuppliesRiskRules verifies the risk rules reach the
// handlers when the config can supply them. main passes one value as both the
// tracker-config source and the risk-rules source; this proves NewMux wires
// the second role, which is invisible from the route table alone.
func TestNewMux_ConfigSuppliesRiskRules(t *testing.T) {
	t.Parallel()

	cfg := &riskAwareConfigSnapshot{
		fakeConfigSnapshot: fakeConfigSnapshot{cfg: &config.Config{}},
		rules:              changeset.DefaultRiskRules(),
	}

	deps := muxDeps(t, newTestStore(t))
	deps.Config = cfg

	mux, err := web.NewMux(deps)
	if err != nil {
		t.Fatalf("web.NewMux: %v", err)
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/changesets", nil))

	if !cfg.rulesRead() {
		t.Error("serving /api/changesets never read the config's risk rules — the RiskRulesSource role is not wired")
	}
}
