package chartrender_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
)

// writeChartWithTemplate writes a minimal, dependency-free chart whose single
// template file is raw, so a test can control exactly what Helm renders
// (kind/namespace/name ordering, key ordering, and so on) without templating
// getting in the way.
func writeChartWithTemplate(t *testing.T, rawTemplate string) (chartDir string) {
	t.Helper()

	dir := t.TempDir()
	chartYAML := `apiVersion: v2
name: normalize-chart
version: 0.1.0
`
	mustWriteFile(t, filepath.Join(dir, "Chart.yaml"), chartYAML)
	mustWriteFile(t, filepath.Join(dir, "templates", "resources.yaml"), rawTemplate)

	return dir
}

// TestRender_Normalizes_SortsManifestsByKindNamespaceName proves acceptance
// criterion 1's sort behavior: the normalized manifest set is ordered by
// (Kind, Namespace, Name) rather than the order the raw render happened to
// produce documents in.
func TestRender_Normalizes_SortsManifestsByKindNamespaceName(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	// Declaration order is deliberately the opposite of the expected sorted
	// order: Service before ConfigMap, and within ConfigMap, "bbb" before
	// "aaa".
	chartDir := writeChartWithTemplate(t, `apiVersion: v1
kind: Service
metadata:
  name: zzz-svc
spec:
  clusterIP: None
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: bbb-cm
data:
  x: "1"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: aaa-cm
data:
  y: "2"
`)

	result, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if len(result.Manifests) != 3 {
		t.Fatalf("len(result.Manifests) = %d, want 3:\n%s", len(result.Manifests), result.Normalized())
	}

	wantOrder := []struct{ kind, name string }{
		{"ConfigMap", "aaa-cm"},
		{"ConfigMap", "bbb-cm"},
		{"Service", "zzz-svc"},
	}
	for i, want := range wantOrder {
		got := result.Manifests[i]
		if got.Kind != want.kind || got.Name != want.name {
			t.Errorf("Manifests[%d] = (kind=%q, name=%q), want (kind=%q, name=%q)", i, got.Kind, got.Name, want.kind, want.name)
		}
	}
}

// TestRender_Normalizes_CanonicalKeyOrdering proves acceptance criterion 1's
// re-serialization behavior: each manifest is re-serialized with
// deterministic (alphabetical) key ordering, regardless of the key order the
// chart source declared.
func TestRender_Normalizes_CanonicalKeyOrdering(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	// data's keys are declared in reverse-alphabetical order.
	chartDir := writeChartWithTemplate(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: order-cm
data:
  zeta: "1"
  alpha: "2"
`)

	result, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(result.Manifests) = %d, want 1:\n%s", len(result.Manifests), result.Normalized())
	}

	want := "apiVersion: v1\ndata:\n  alpha: \"2\"\n  zeta: \"1\"\nkind: ConfigMap\nmetadata:\n  name: order-cm\n"
	if got := result.Manifests[0].YAML; got != want {
		t.Errorf("Manifests[0].YAML =\n%s\nwant\n%s", got, want)
	}
}

// TestRender_Normalizes_DeterministicAcrossRenders proves the acceptance
// criterion's core promise: two Render calls against the same chart and
// values produce byte-identical normalized output, which is what lets
// manifestdiff line-diff two manifest sets without spurious noise.
func TestRender_Normalizes_DeterministicAcrossRenders(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	chartDir := writeChartWithTemplate(t, `apiVersion: v1
kind: Service
metadata:
  name: zzz-svc
spec:
  clusterIP: None
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: bbb-cm
data:
  x: "1"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: aaa-cm
data:
  y: "2"
`)

	first, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render (first): %v", err)
	}
	second, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render (second): %v", err)
	}

	if first.Normalized() != second.Normalized() {
		t.Errorf("Normalized() differs across renders of the same chart:\nfirst:\n%s\nsecond:\n%s", first.Normalized(), second.Normalized())
	}
}

// TestRender_Normalizes_DropsEmptyDocumentsAndSourceComments proves
// acceptance criterion 1's cleanup behavior: a template that conditionally
// renders nothing produces no manifest entry (rather than a stray blank/
// comment-only one), and Helm's own "# Source: <path>" bookkeeping comment
// never survives into a manifest's normalized YAML.
func TestRender_Normalizes_DropsEmptyDocumentsAndSourceComments(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	chartDir := writeChartWithTemplate(t, `{{- if false }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: never-rendered
{{- end }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: real-cm
data:
  present: "yes"
`)

	result, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(result.Manifests) = %d, want 1 (the conditionally-empty document must be dropped):\n%s", len(result.Manifests), result.Normalized())
	}
	if got := result.Manifests[0].Name; got != "real-cm" {
		t.Errorf("Manifests[0].Name = %q, want %q", got, "real-cm")
	}
	if strings.Contains(result.Normalized(), "# Source:") {
		t.Errorf("Normalized() retained a Helm \"# Source:\" bookkeeping comment:\n%s", result.Normalized())
	}
}

// TestRender_Normalizes_PreservesLargeIntegerPrecision proves the "canonical,
// lossless" promise: an unquoted integer above 2^53 (float64's exact-integer
// range) survives re-serialization with its exact digits, rather than being
// rounded through a float64 intermediate representation. A round-trip through
// float64 would silently corrupt the manifest and could make two distinct
// large integers indistinguishable downstream in manifestdiff.
func TestRender_Normalizes_PreservesLargeIntegerPrecision(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	chartDir := writeChartWithTemplate(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: bignum-cm
unquotedBig: 123456789012345678
`)

	result, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(result.Manifests) = %d, want 1:\n%s", len(result.Manifests), result.Normalized())
	}
	if !strings.Contains(result.Manifests[0].YAML, "unquotedBig: 123456789012345678") {
		t.Errorf("Manifests[0].YAML lost precision on a large integer, want exact digits preserved:\n%s", result.Manifests[0].YAML)
	}
}

// TestRender_Normalizes_ExcludesHookOnlyResources proves that a
// hook-annotated resource (e.g. a pre-install Job) — which Helm's own
// renderResources already routes to a separate hooks list rather than the
// raw manifest text Render normalizes — never appears in the normalized
// manifest set.
func TestRender_Normalizes_ExcludesHookOnlyResources(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	chartDir := writeChartWithTemplate(t, `apiVersion: batch/v1
kind: Job
metadata:
  name: pre-install-hook-job
  annotations:
    "helm.sh/hook": pre-install
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: hook
          image: busybox
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: real-cm
`)

	result, err := chartrender.Render(chartDir, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(result.Manifests) = %d, want 1 (the hook resource must be excluded):\n%s", len(result.Manifests), result.Normalized())
	}
	if got := result.Manifests[0].Name; got != "real-cm" {
		t.Errorf("Manifests[0].Name = %q, want %q", got, "real-cm")
	}
}
