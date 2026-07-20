package chartdiff_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/chartdiff"
	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
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
// Deployment template whose replica count is baked into the package) as
// "<name>-<version>.tgz" into chartsDir, offline — the same on-disk shape a
// real repo's committed charts/*.tgz vendored dependency has.
func packageSubchart(t *testing.T, chartsDir, name, version string, replicas int) {
	t.Helper()

	srcDir := t.TempDir()
	realTestWriteFile(t, filepath.Join(srcDir, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: "+version+"\n")
	realTestWriteFile(t, filepath.Join(srcDir, "templates", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: `+name+`-deployment
spec:
  replicas: `+itoa(replicas)+`
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

func itoa(n int) string {
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

// buildDepBumpAndVendoredChartSwapRepo builds a temp git repo with a single
// tenant directory ("tenant/") across two commits, matching the PRD's
// headline Chart diff scenario: an umbrella chart with its own template and
// committed values.yaml (identical on both commits), whose declared
// dependency is bumped from 0.1.0 (1 replica) to 0.2.0 (2 replicas), with
// the vendored charts/*.tgz swapped to match. Returns the repo path and both
// commit SHAs.
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

	// Commit 1: dependency "sub" at 0.1.0 (1 replica).
	realTestWriteFile(t, filepath.Join(tenantDir, "Chart.yaml"), umbrellaChartYAML("0.1.0"))
	packageSubchart(t, filepath.Join(tenantDir, "charts"), "sub", "0.1.0", 1)
	addAll(t, wt, dir)
	c1, err := wt.Commit("chore: seed tenant umbrella chart (sub 0.1.0)", &git.CommitOptions{
		Author: &object.Signature{Name: "alice", Email: "alice@example.com", When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}

	// Commit 2: bump "sub" to 0.2.0 (2 replicas) — swap the vendored tgz,
	// leave values.yaml and the umbrella's own template untouched.
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

// TestDiff_RealRepo_SuccessPath_DepBumpAndVendoredChartSwap is the PRD's
// headline scenario, exercised through the real gitsource.Source and the
// real chartrender.Render (via a nil Renderer, so NewEngine wires the
// production adapter) — no fakes on either seam. It proves: the tenant
// umbrella chart renders at first-parent vs at the commit using the
// tenant's own committed values.yaml, and only the bumped vendored
// subchart's manifest differs.
func TestDiff_RealRepo_SuccessPath_DepBumpAndVendoredChartSwap(t *testing.T) {
	t.Parallel()

	repoPath, sha1, sha2 := buildDepBumpAndVendoredChartSwapRepo(t)
	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), src, chartdiff.Request{
		RepoName:   "tenant-repo",
		TenantPath: "tenant",
		CommitSha:  sha2,
	})

	if outcome.Kind != chartdiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q (sha1=%s sha2=%s)", outcome.Kind, chartdiff.OK, sha1, sha2)
	}
	if outcome.Diff.Summary.ManifestsChanged != 1 {
		t.Errorf("Summary.ManifestsChanged = %d, want 1 (only the vendored subchart's Deployment changed)", outcome.Diff.Summary.ManifestsChanged)
	}
	if strings.Contains(outcome.Diff.Unified, "umbrella-configmap") {
		t.Errorf("Unified diff mentions the unchanged umbrella-configmap manifest, want no diff for it:\n%s", outcome.Diff.Unified)
	}
	if !strings.Contains(outcome.Diff.Unified, "-  replicas: 1") || !strings.Contains(outcome.Diff.Unified, "+  replicas: 2") {
		t.Errorf("Unified diff missing the expected subchart replica-count change:\n%s", outcome.Diff.Unified)
	}
}

// TestDiff_RealRepo_RootCommit_ReturnsNoPriorVersion proves the NoPriorVersion
// classification through the real gitsource.Source (not a fake): a
// single-commit repo's only commit has no first parent.
func TestDiff_RealRepo_RootCommit_ReturnsNoPriorVersion(t *testing.T) {
	t.Parallel()

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
	realTestWriteFile(t, filepath.Join(tenantDir, "Chart.yaml"), "apiVersion: v2\nname: umbrella\nversion: 0.1.0\n")
	realTestWriteFile(t, filepath.Join(tenantDir, "values.yaml"), "message: hello-tenant\n")
	addAll(t, wt, dir)
	rootCommit, err := wt.Commit("chore: seed tenant chart (root commit)", &git.CommitOptions{
		Author: &object.Signature{Name: "alice", Email: "alice@example.com", When: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	src, err := gitsource.Open(dir)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), src, chartdiff.Request{
		RepoName:   "tenant-repo",
		TenantPath: "tenant",
		CommitSha:  rootCommit.String(),
	})

	if outcome.Kind != chartdiff.NoPriorVersion {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, chartdiff.NoPriorVersion)
	}
}

// TestDiff_RealRepo_MaterializationBoundExceeded_ReturnsExceededLimits proves
// Config's materialization ceilings (MaxMaterializedBytes/Files/Depth) are
// really wired end-to-end from Engine.Diff through to a real
// gitsource.Source's MaterializeSubtreeBounded — not just proven against a
// fakeChartRepo that returns the sentinel error directly (as
// classification_test.go and classification_property_test.go do). A tiny
// MaxMaterializedBytes against the real, vendored-tgz-carrying tenant
// subtree from buildDepBumpAndVendoredChartSwapRepo must exceed the ceiling
// and classify as ExceededLimits.
func TestDiff_RealRepo_MaterializationBoundExceeded_ReturnsExceededLimits(t *testing.T) {
	t.Parallel()

	repoPath, _, sha2 := buildDepBumpAndVendoredChartSwapRepo(t)

	src, err := gitsource.Open(repoPath)
	if err != nil {
		t.Fatalf("gitsource.Open: %v", err)
	}

	engine, err := chartdiff.NewEngine(chartdiff.Config{
		MaxMaterializedBytes: 1, // far smaller than the real tenant subtree (Chart.yaml alone exceeds this)
		MaxMaterializedFiles: 1000,
		MaxMaterializedDepth: 20,
	}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), src, chartdiff.Request{
		RepoName:   "tenant-repo",
		TenantPath: "tenant",
		CommitSha:  sha2,
	})

	if outcome.Kind != chartdiff.ExceededLimits {
		t.Errorf("outcome.Kind = %q, want %q (a 1-byte MaxMaterializedBytes ceiling against a real tenant subtree with real vendored content)", outcome.Kind, chartdiff.ExceededLimits)
	}
}
