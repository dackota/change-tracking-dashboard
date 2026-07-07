package chartrender_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
)

// TestRender_MalformedChart_ClassifiesFailure proves acceptance criterion 2:
// a chart that cannot be loaded, or that fails to render for a chart-content
// reason, produces a classified *Failure with Reason ==
// ReasonMalformedChart — never an opaque wrapped error and never a partial
// Result.
func TestRender_MalformedChart_ClassifiesFailure(t *testing.T) {
	tests := []struct {
		name     string
		chartDir func(t *testing.T) string
	}{
		{
			name: "unloadable chart: Chart.yaml is not valid YAML",
			chartDir: func(t *testing.T) string {
				dir := t.TempDir()
				mustWriteFile(t, filepath.Join(dir, "Chart.yaml"), "not: [valid: yaml")
				return dir
			},
		},
		{
			name: "unloadable chart: Chart.yaml missing entirely",
			chartDir: func(t *testing.T) string {
				dir := t.TempDir()
				mustWriteFile(t, filepath.Join(dir, "values.yaml"), "message: hi\n")
				return dir
			},
		},
		{
			name: "template render failure: invalid template syntax",
			chartDir: func(t *testing.T) string {
				dir := t.TempDir()
				mustWriteFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: broken-template-chart\nversion: 0.1.0\n")
				mustWriteFile(t, filepath.Join(dir, "templates", "bad.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Values.Name\n")
				return dir
			},
		},
		{
			name: "template render failure: rendered output is not valid YAML",
			chartDir: func(t *testing.T) string {
				dir := t.TempDir()
				mustWriteFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: invalid-yaml-output-chart\nversion: 0.1.0\n")
				mustWriteFile(t, filepath.Join(dir, "templates", "bad.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata: [this, is, a, list, not, a, map]\n")
				return dir
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which
			// forbids it.
			blockNetworkAndCluster(t)

			chartDir := tt.chartDir(t)

			result, err := chartrender.Render(chartDir, nil)
			if result != nil {
				t.Errorf("Render result = %+v, want nil on malformed-chart failure", result)
			}
			if err == nil {
				t.Fatal("Render err = nil, want a classified malformed-chart failure")
			}

			var failure *chartrender.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("Render err = %v (%T), want *chartrender.Failure", err, err)
			}
			if failure.Reason != chartrender.ReasonMalformedChart {
				t.Errorf("failure.Reason = %q, want %q", failure.Reason, chartrender.ReasonMalformedChart)
			}
			if failure.ChartDir != chartDir {
				t.Errorf("failure.ChartDir = %q, want %q", failure.ChartDir, chartDir)
			}
		})
	}
}

// TestRender_MalformedChart_NoKindDocumentWithContentClassifiesFailure proves
// that a stray mid-manifest "---" that splits one Kubernetes object into two
// documents — the first carrying kind/metadata, the second carrying only
// unrelated fields (real content, but no kind) — is never silently dropped as
// if it were an empty/bookkeeping-only document. Render must classify this as
// a malformed-chart failure rather than return a truncated Result, honoring
// the package's documented "never a partial result" invariant.
func TestRender_MalformedChart_NoKindDocumentWithContentClassifiesFailure(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: stray-separator-chart\nversion: 0.1.0\n")
	mustWriteFile(t, filepath.Join(dir, "templates", "resources.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: split-cm
---
data:
  foo: bar
`)

	result, err := chartrender.Render(dir, nil)
	if result != nil {
		t.Errorf("Render result = %+v, want nil on malformed-chart failure (a stray \"---\" must never yield a truncated success)", result)
	}
	if err == nil {
		t.Fatal("Render err = nil, want a classified malformed-chart failure")
	}

	var failure *chartrender.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("Render err = %v (%T), want *chartrender.Failure", err, err)
	}
	if failure.Reason != chartrender.ReasonMalformedChart {
		t.Errorf("failure.Reason = %q, want %q", failure.Reason, chartrender.ReasonMalformedChart)
	}
	if failure.ChartDir != dir {
		t.Errorf("failure.ChartDir = %q, want %q", failure.ChartDir, dir)
	}
}

// TestRender_MalformedChart_DependencyNotVendoredTakesPrecedence proves that
// the dependency-not-vendored check still fires first: a chart missing a
// vendored dependency is classified as dependency-not-vendored even though,
// absent that check, attempting to render it would also fail (Helm would try
// to reach a registry). This preserves the acceptance-criteria ordering
// requirement explicitly.
func TestRender_MalformedChart_DependencyNotVendoredTakesPrecedence(t *testing.T) {
	// Not t.Parallel(): blockNetworkAndCluster uses t.Setenv, which forbids it.
	blockNetworkAndCluster(t)

	chartDir := writeUmbrellaChart(t, "unvendored-subchart", "1.0.0")

	_, err := chartrender.Render(chartDir, nil)
	if err == nil {
		t.Fatal("Render err = nil, want a classified failure")
	}

	var failure *chartrender.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("Render err = %v (%T), want *chartrender.Failure", err, err)
	}
	if failure.Reason != chartrender.ReasonDependencyNotVendored {
		t.Errorf("failure.Reason = %q, want %q (not %q)", failure.Reason, chartrender.ReasonDependencyNotVendored, chartrender.ReasonMalformedChart)
	}
}
