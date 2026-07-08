//go:build regression

// Package web (this file): the R11-R15 preserved-interaction regression
// harness for the visual-system-regression slice. It is deliberately gated
// behind the "regression" build tag — this project's CI (go build/test/vet +
// Trivy fs scan; see .github/workflows/pr-ci.yml) has no Node/Chrome
// toolchain, and `go test ./...` (no tags) must never depend on one. This
// file only compiles, and TestUIRegression_PreservedInteractions
// only runs, when a human explicitly opts in:
//
//	npm install --no-save playwright-core   # one-time; writes only
//	                                         # node_modules/ (gitignored),
//	                                         # never a package.json/lock
//	go test -race -tags regression -run TestUIRegression -v ./internal/web/...
//
// It boots the dashboard's real, production HTTP surface — the exact
// handlers cmd/dashboard/main.go wires into its mux (TimelineHandler,
// StaticHandler, ChangesetsHandler, ChangesetDetailHandler,
// ChartDiffHandler) — against a seeded store plus a real, temporary git repo
// carrying an actual chart-version bump (the same fixture shape as
// chart_diff_realrepo_test.go's buildDepBumpAndVendoredChartSwapRepo, reused
// directly), then drives a headless system Chrome via playwright-core
// (internal/web/static/timeline.regression.js) to prove the PRD's preserved
// interactions still work end-to-end with no behavior regression:
//
//   - R11: facet dropdowns cycle include -> exclude -> off, the "only"
//     shortcut, per-facet active-count badges, single Clear filters.
//   - R12/R13: dated day/time axis; drag-to-zoom, scroll-to-zoom,
//     shift-drag pan, Reset zoom; From/To inputs mirror the window.
//   - R14: clicking a cluster marker zooms so stacked flags split apart;
//     flags re-cluster on render.
//   - R15: clicking a Changeset (marker or feed row) opens detail;
//     Chart-kind Changes render the collapsed red/green hunked Chart diff.
//
// If system Chrome, Node, or playwright-core is unavailable, the test SKIPs
// (never fails) with the exact remediation step — mirroring this project's
// own runtime-validator convention that a missing local dependency is a
// SKIP, not a BLOCK.
package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/pollstatus"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// regressionHarnessTimeout bounds the whole Node-driven browser run so a
// hung browser/page never hangs `go test` indefinitely.
const regressionHarnessTimeout = 2 * time.Minute

// regressionClusterGapSeconds is how close together the two "cluster"
// Changesets are seeded (R14 needs them to collapse into one marker at the
// full-zoom-out view, well under timeline.js's CLUSTER_PIXEL_RADIUS at the
// default track width).
const regressionClusterGapSeconds = 20

// TestUIRegression_PreservedInteractions is the harness entry point: seed a
// realistic dataset, boot the real HTTP handlers, then hand off to the
// checked-in Node/Playwright script to drive and assert the preserved
// interactions in a real, headless system Chrome.
func TestUIRegression_PreservedInteractions(t *testing.T) {
	scriptDir := regressionScriptDir(t)
	requireNodeHarnessToolchain(t, scriptDir)
	chrome := resolveSystemChrome(t)

	srv, chartBumpSHA := newRegressionServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), regressionHarnessTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "node", filepath.Join(scriptDir, "timeline.regression.js"))
	cmd.Env = append(os.Environ(),
		"BASE_URL="+srv.URL,
		"CHROME_PATH="+chrome,
		"CHART_BUMP_SHA="+chartBumpSHA,
	)

	out, err := cmd.CombinedOutput()
	t.Logf("UI regression harness output:\n%s", out)
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("UI regression harness timed out after %s", regressionHarnessTimeout)
	}
	if err != nil {
		t.Fatalf("UI regression harness failed: %v", err)
	}
}

// regressionScriptDir resolves the directory containing the Node harness
// script, relative to this test file rather than the process's working
// directory (which `go test` sets to the package directory anyway, but
// resolving from the caller's own file makes this independent of that
// assumption).
func regressionScriptDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve regression harness script directory")
	}
	return filepath.Join(filepath.Dir(thisFile), "static")
}

// requireNodeHarnessToolchain SKIPs with a precise remediation step when
// Node or playwright-core is not available from scriptDir — this harness is
// a local/validation-time proof, never a CI dependency (see the package doc
// comment above).
func requireNodeHarnessToolchain(t *testing.T, scriptDir string) {
	t.Helper()

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH — install Node 18+ to run the UI regression harness")
	}

	check := exec.Command("node", "-e", "require.resolve('playwright-core')")
	check.Dir = scriptDir
	if out, err := check.CombinedOutput(); err != nil {
		t.Skipf("playwright-core is not resolvable from %s — run:\n\n"+
			"  npm install --no-save playwright-core\n\n"+
			"from that directory (writes only a gitignored node_modules/, no "+
			"package.json/lock) and re-run this test.\n%s", scriptDir, out)
	}
}

// resolveSystemChrome finds a system Chrome/Chromium executable to hand to
// playwright-core (this harness never downloads a browser — see the PRD's
// "system Chrome via playwright-core" mechanism). CHROME_PATH always wins;
// otherwise a handful of conventional install locations/binary names are
// tried across macOS and Linux. SKIPs (does not fail) when none is found.
func resolveSystemChrome(t *testing.T) string {
	t.Helper()

	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}

	for _, candidatePath := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", // macOS
	} {
		if info, err := os.Stat(candidatePath); err == nil && !info.IsDir() {
			return candidatePath
		}
	}

	for _, bin := range []string{
		"google-chrome", "google-chrome-stable", // common Linux install names
		"chromium", "chromium-browser",
	} {
		if p, err := exec.LookPath(bin); err == nil {
			return p
		}
	}

	t.Skip("no system Chrome/Chromium found — set CHROME_PATH to a system Chrome/Chromium executable and re-run")
	return ""
}

// newRegressionServer boots an httptest.Server wired with the exact
// production handler set cmd/dashboard/main.go assembles into its mux,
// backed by a store seeded via seedRegressionDataset. It returns the server
// (closed automatically via t.Cleanup) and the full commit sha of the
// seeded chart-bump Changeset, so the Node harness can locate that specific
// row/marker without depending on any other seeded data's shape.
func newRegressionServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	st := newTestStore(t)
	chartBumpSHA := seedRegressionDataset(t, st)

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, nil)
	if err != nil {
		t.Fatalf("chartdiff.NewEngine: %v", err)
	}
	resolver := &fakeChartRepoResolver{fn: func(repo string) (chartdiff.ChartRepo, error) {
		src, err := gitsource.Open(repo)
		if err != nil {
			return nil, err
		}
		return src, nil
	}}

	mux := http.NewServeMux()
	mux.Handle("/", web.NewTimelineHandler(st, pollstatus.New()))
	mux.Handle("/static/", web.NewStaticHandler())
	mux.Handle("/api/changesets", web.NewChangesetsHandler(st))
	mux.Handle("/api/changesets/detail", web.NewChangesetDetailHandler(st))
	mux.Handle("/api/changesets/detail/chart-diff", web.NewChartDiffHandler(engine, resolver, st))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, chartBumpSHA
}

// seedRegressionDataset saves a realistic Changeset history spanning two
// facets (component, layer) and a wide time range, so every preserved
// interaction has something real to exercise:
//   - two well-separated older Changesets (so the timeline has a real span
//     to zoom/pan/reset across, and the feed has rows to filter);
//   - two Changesets ~20s apart (R14: collapse into one cluster marker at
//     the full-zoom-out view, split apart on click);
//   - one real chart-version-bump Changeset in a real, temporary git repo
//     (R15: an actual "ok" Chart diff with real +/- hunks, not a fake).
//
// Returns the chart-bump Changeset's full commit sha.
func seedRegressionDataset(t *testing.T, st *store.Store) string {
	t.Helper()
	now := time.Now()
	const webRepo = "https://github.com/example/webapp"

	save := func(commitSha, filePath, author string, facets map[string]string, age time.Duration, repo string) {
		t.Helper()
		c := domain.Change{
			Repo:        repo,
			FilePath:    filePath,
			Field:       "f",
			ChangeType:  domain.ChangeTypeModified,
			OldValue:    ptr("old-" + commitSha),
			NewValue:    ptr("new-" + commitSha),
			Facets:      facets,
			CommitSha:   commitSha,
			Author:      author,
			CommittedAt: now.Add(-age),
		}
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("seed %s: %v", commitSha, err)
		}
	}

	save("c-old-1", "values.yaml", "alice", map[string]string{"component": "web", "layer": "frontend"}, 30*24*time.Hour, webRepo)
	save("c-old-2", "values.yaml", "bob", map[string]string{"component": "api", "layer": "backend"}, 20*24*time.Hour, webRepo)
	// A tight cluster: two commits ~20s apart, well within CLUSTER_PIXEL_RADIUS
	// at the full-window zoom level (R14).
	save("c-cluster-1", "values.yaml", "carol", map[string]string{"component": "web", "layer": "frontend"}, 2*24*time.Hour, webRepo)
	save("c-cluster-2", "values.yaml", "carol", map[string]string{"component": "api", "layer": "backend"}, 2*24*time.Hour-regressionClusterGapSeconds*time.Second, webRepo)

	// A real chart-version bump (R15) in a real, temporary git fixture repo
	// (reuses chart_diff_realrepo_test.go's own fixture builder) so the
	// Chart diff renders an actual "ok" outcome with real hunks, not a fake.
	// No facets: it must not add a third value to the component/layer
	// dropdowns the facet-filtering assertions (R11) exercise above.
	repoPath, _, sha2 := buildDepBumpAndVendoredChartSwapRepo(t)
	save(sha2, "tenant/Chart.yaml", "dana", nil, 24*time.Hour, repoPath)

	return sha2
}
