package chartrender_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
)

// blockNetworkAndCluster makes it impossible for a wayward code path to reach
// a real Kubernetes cluster or the network during a test: KUBECONFIG points at
// a file that does not exist, and both proxy env vars point at an address
// nothing listens on. If Render ever regresses into contacting a cluster or
// dialing out, the test fails loudly (dial/connection error) instead of
// silently succeeding against a real environment.
func blockNetworkAndCluster(t *testing.T) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "no-such-kubeconfig"))
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
}

// writeSimpleChart writes a minimal, dependency-free chart (Chart.yaml +
// values.yaml + a single ConfigMap template) to a temp directory and returns
// the chart directory path.
func writeSimpleChart(t *testing.T) (chartDir string) {
	t.Helper()

	dir := t.TempDir()

	chartYAML := `apiVersion: v2
name: spike-chart
version: 0.1.0
`
	valuesYAML := `message: hello-from-chartrender-spike
`
	configMapTemplate := `apiVersion: v1
kind: ConfigMap
metadata:
  name: spike-chart-configmap
data:
  message: {{ .Values.message | quote }}
`

	mustWriteFile(t, filepath.Join(dir, "Chart.yaml"), chartYAML)
	mustWriteFile(t, filepath.Join(dir, "values.yaml"), valuesYAML)
	mustWriteFile(t, filepath.Join(dir, "templates", "configmap.yaml"), configMapTemplate)

	return dir
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

// packageVendoredSubchart writes a tiny standalone subchart's source
// (Chart.yaml + a Deployment template) to a scratch directory, then packages
// it — offline, via the same tar/gzip logic `helm package` uses — as
// "<name>-<version>.tgz" into chartsDir. This is how a real repo's committed
// charts/*.tgz vendored dependency comes to exist; the packaging itself never
// touches the network.
func packageVendoredSubchart(t *testing.T, chartsDir, name, version string) {
	t.Helper()

	srcDir := t.TempDir()
	chartYAML := "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n"
	deploymentTemplate := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + name + `-deployment
spec:
  replicas: 1
`
	mustWriteFile(t, filepath.Join(srcDir, "Chart.yaml"), chartYAML)
	mustWriteFile(t, filepath.Join(srcDir, "templates", "deployment.yaml"), deploymentTemplate)

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

// writeUmbrellaChart writes an umbrella chart declaring a dependency on
// (depName, depVersion) in Chart.yaml, plus its own template. It does NOT
// vendor the dependency — callers that want a vendored dependency must also
// call packageVendoredSubchart(t, filepath.Join(dir, "charts"), depName, depVersion).
func writeUmbrellaChart(t *testing.T, depName, depVersion string) (chartDir string) {
	t.Helper()

	dir := t.TempDir()
	chartYAML := `apiVersion: v2
name: umbrella-chart
version: 0.1.0
dependencies:
  - name: ` + depName + `
    version: "` + depVersion + `"
    repository: "https://example.invalid/charts"
`
	umbrellaTemplate := `apiVersion: v1
kind: ConfigMap
metadata:
  name: umbrella-chart-configmap
data:
  owner: umbrella
`
	mustWriteFile(t, filepath.Join(dir, "Chart.yaml"), chartYAML)
	mustWriteFile(t, filepath.Join(dir, "templates", "configmap.yaml"), umbrellaTemplate)

	return dir
}

// TestRender_VendoredDependency_RendersOffline proves acceptance criterion
// (b): an umbrella chart whose declared dependency is satisfied by a
// committed charts/<subchart>.tgz renders — with no network and no OCI pull —
// and the subchart's own rendered manifest appears alongside the umbrella
// chart's own manifest.
func TestRender_VendoredDependency_RendersOffline(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	chartDir := writeUmbrellaChart(t, "spike-subchart", "0.2.0")
	packageVendoredSubchart(t, filepath.Join(chartDir, "charts"), "spike-subchart", "0.2.0")

	result, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	normalized := result.Normalized()
	if !strings.Contains(normalized, "name: umbrella-chart-configmap") {
		t.Errorf("Normalized() missing umbrella chart's own resource:\n%s", normalized)
	}
	if !strings.Contains(normalized, "name: spike-subchart-deployment") {
		t.Errorf("Normalized() missing vendored subchart's resource — offline render of committed charts/*.tgz failed:\n%s", normalized)
	}
}

// TestRender_DependencyNotVendored_ClassifiesFailure proves acceptance
// criterion (c): a chart that declares a dependency in Chart.yaml with no
// corresponding vendored charts/*.tgz — i.e. Helm would have to pull it from
// a registry to render — produces a classified dependency-not-vendored
// failure rather than a partial render or an opaque error.
func TestRender_DependencyNotVendored_ClassifiesFailure(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	// Declare the dependency but never call packageVendoredSubchart — no
	// charts/*.tgz is ever written for it.
	chartDir := writeUmbrellaChart(t, "unvendored-subchart", "1.0.0")

	result, err := chartrender.Render(chartDir, nil)
	if result != nil {
		t.Errorf("Render result = %+v, want nil on dependency-not-vendored failure", result)
	}
	if err == nil {
		t.Fatal("Render err = nil, want a classified dependency-not-vendored failure")
	}

	var failure *chartrender.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("Render err = %v (%T), want *chartrender.Failure", err, err)
	}
	if failure.Reason != chartrender.ReasonDependencyNotVendored {
		t.Errorf("failure.Reason = %q, want %q", failure.Reason, chartrender.ReasonDependencyNotVendored)
	}
	if len(failure.Missing) != 1 || failure.Missing[0] != "unvendored-subchart" {
		t.Errorf("failure.Missing = %v, want [unvendored-subchart]", failure.Missing)
	}
}

// TestRender_ClientOnly_NoClusterContact_RendersManifest proves acceptance
// criterion (a): the Helm SDK renders a chart to Kubernetes manifests
// client-only, with no cluster/kube-API contact. blockNetworkAndCluster makes
// any accidental cluster/network dependency fail the test instead of silently
// passing.
func TestRender_ClientOnly_NoClusterContact_RendersManifest(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	chartDir := writeSimpleChart(t)

	result, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(result.Manifests) = %d, want 1:\n%s", len(result.Manifests), result.Normalized())
	}
	got := result.Manifests[0]
	if got.Kind != "ConfigMap" {
		t.Errorf("Manifests[0].Kind = %q, want %q", got.Kind, "ConfigMap")
	}
	if got.Name != "spike-chart-configmap" {
		t.Errorf("Manifests[0].Name = %q, want %q", got.Name, "spike-chart-configmap")
	}
	if !strings.Contains(got.YAML, `message: hello-from-chartrender-spike`) {
		t.Errorf("Manifests[0].YAML missing expected rendered value:\n%s", got.YAML)
	}
}
