package web_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/web"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
)

// realTestWriteFile writes content to path, creating parent directories as
// needed.
func realTestWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

// packageSubchart packages a tiny standalone subchart (Chart.yaml + a
// Deployment template whose replica count is baked in) as
// "<name>-<version>.tgz" into chartsDir, offline — the same on-disk shape a
// real repo's committed charts/*.tgz vendored dependency has. Mirrors
// chartdiff's own realrepo_test.go helper of the same name.
func packageSubchart(t *testing.T, chartsDir, name, version string, replicas int) {
	t.Helper()

	srcDir := t.TempDir()
	realTestWriteFile(t, filepath.Join(srcDir, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: "+version+"\n")
	realTestWriteFile(t, filepath.Join(srcDir, "templates", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: `+name+`-deployment
spec:
  replicas: `+itoaHelper(replicas)+`
`)

	sub, err := loader.LoadDir(srcDir)
	if err != nil {
		t.Fatalf("loader.LoadDir(subchart source): %v", err)
	}
	if err := os.MkdirAll(chartsDir, 0o755); err != nil {
		t.Fatalf("mkdir chartsDir: %v", err)
	}
	if _, err := chartutil.Save(sub, chartsDir); err != nil {
		t.Fatalf("chartutil.Save(subchart): %v", err)
	}
}

func itoaHelper(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// addAll stages every file currently on disk under dir into the worktree
// index, in deterministic (sorted) order.
func addAll(t *testing.T, wt *git.Worktree, dir string) {
	t.Helper()

	var relPaths []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.Contains(path, string(filepath.Separator)+".git") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		relPaths = append(relPaths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo dir: %v", err)
	}
	sort.Strings(relPaths)

	for _, rel := range relPaths {
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("git add %q: %v", rel, err)
		}
	}
}

// buildDepBumpAndVendoredChartSwapRepo builds a temp git repo with a single
// tenant directory ("tenant/") across two commits: an umbrella chart with
// its own template and committed values.yaml (identical on both commits),
// whose declared dependency is bumped from 0.1.0 (1 replica) to 0.2.0
// (2 replicas), with the vendored charts/*.tgz swapped to match. Mirrors
// chartdiff's own realrepo_test.go fixture of the same name. Returns the
// repo path and both commit SHAs.
func buildDepBumpAndVendoredChartSwapRepo(t *testing.T) (repoPath, sha1, sha2 string) {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	tenantDir := filepath.Join(dir, "tenant")
	umbrellaChartYAML := func(depVersion string) string {
		return `apiVersion: v2
name: umbrella
version: 0.1.0
dependencies:
  - name: sub
    version: "` + depVersion + `"
    repository: "https://example.invalid/charts"
`
	}
	realTestWriteFile(t, filepath.Join(tenantDir, "values.yaml"), "message: hello-tenant\n")
	realTestWriteFile(t, filepath.Join(tenantDir, "templates", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: umbrella-configmap
data:
  message: {{ .Values.message | quote }}
`)

	realTestWriteFile(t, filepath.Join(tenantDir, "Chart.yaml"), umbrellaChartYAML("0.1.0"))
	packageSubchart(t, filepath.Join(tenantDir, "charts"), "sub", "0.1.0", 1)
	addAll(t, wt, dir)
	c1, err := wt.Commit("chore: seed tenant umbrella chart (sub 0.1.0)", &git.CommitOptions{
		Author: &object.Signature{Name: "alice", Email: "alice@example.com", When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}

	if _, err := wt.Remove(filepath.ToSlash(filepath.Join("tenant", "charts", "sub-0.1.0.tgz"))); err != nil {
		t.Fatalf("git rm old vendored chart: %v", err)
	}
	realTestWriteFile(t, filepath.Join(tenantDir, "Chart.yaml"), umbrellaChartYAML("0.2.0"))
	packageSubchart(t, filepath.Join(tenantDir, "charts"), "sub", "0.2.0", 2)
	addAll(t, wt, dir)
	c2, err := wt.Commit("feat: bump sub 0.1.0 -> 0.2.0", &git.CommitOptions{
		Author: &object.Signature{Name: "bob", Email: "bob@example.com", When: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}

	return dir, c1.String(), c2.String()
}

// TestChartDiffHandler_RealRepo_SuccessPath_DepBumpAndVendoredChartSwap is
// the end-to-end proof (real gitsource.Source + a real chartdiff.Engine
// backed by the production Helm renderer — no fakes on either seam) that the
// handler wiring works against real chart content: the endpoint returns the
// diff HTML for a real chart change.
func TestChartDiffHandler_RealRepo_SuccessPath_DepBumpAndVendoredChartSwap(t *testing.T) {
	t.Parallel()

	repoPath, _, sha2 := buildDepBumpAndVendoredChartSwapRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, nil)
	if err != nil {
		t.Fatalf("chartdiff.NewEngine: %v", err)
	}

	// Seed the store with the Changeset the security gate requires: only a
	// (repo, commitSha) pair the poller has already ingested may reach the
	// resolver/engine. This proves the real end-to-end wiring (a real
	// store.Store satisfying web.ChangesetExistenceChecker, exactly as
	// cmd/dashboard wires it) still lets the legitimate path through.
	st := newTestStore(t)
	if err := st.SaveChange(domain.Change{
		Repo:        "tenant-repo",
		FilePath:    "tenant/Chart.yaml",
		Field:       "dependency",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("0.1.0"),
		NewValue:    ptr("0.2.0"),
		CommitSha:   sha2,
		Author:      "bob",
		CommittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed matching changeset: %v", err)
	}

	resolver := &fakeChartRepoResolver{fn: func(string) (chartdiff.ChartRepo, error) { return src, nil }}
	h := web.NewChartDiffHandler(engine, resolver, st)

	url := "/api/changesets/detail/chart-diff?repo=tenant-repo&commitSha=" + sha2 + "&path=tenant"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-kind="ok"`) {
		t.Errorf("body missing data-kind=\"ok\" marker; got:\n%s", body)
	}
	// The removed line's "-" prefix needs no HTML escaping, but the added
	// line's "+" prefix does (html/template escapes '+' to the numeric
	// entity &#43; in text content) — asserting the escaped form here proves
	// the real end-to-end path applies the same auto-escaping the property
	// test (chart_diff_test.go) proves in isolation, not just an unescaped
	// pass-through that happens to look right for most content.
	if !strings.Contains(body, "-  replicas: 1") {
		t.Errorf("body missing expected removed line for the old replica count; got:\n%s", body)
	}
	if !strings.Contains(body, "&#43;  replicas: 2") {
		t.Errorf("body missing expected (HTML-escaped) added line for the new replica count; got:\n%s", body)
	}
	if strings.Contains(body, "umbrella-configmap") {
		t.Errorf("body mentions the unchanged umbrella-configmap manifest, want no diff for it:\n%s", body)
	}
}
