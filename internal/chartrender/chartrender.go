// Package chartrender renders a Helm chart to Kubernetes manifests in-process
// via the embedded Helm v3 SDK. It is the seed of the future chartrender deep
// module (see ADR 0002): render is always client-only and fully offline — no
// Kubernetes API contact, no chart-registry pull. The chart must be fully
// vendored: every dependency declared in Chart.yaml must have a corresponding
// committed charts/*.tgz (or charts/<name>/ directory) artifact. When that is
// not the case, Render returns a classified *Failure instead of attempting a
// partial render or surfacing an opaque Helm error.
package chartrender

import (
	"fmt"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

// Result is a successful, fully offline chart render.
type Result struct {
	// Manifest is the concatenated rendered Kubernetes manifest YAML
	// (multi-document, "---"-separated), the same text `helm template`
	// would print.
	Manifest string
}

// FailureReason classifies why a chart could not be rendered, so a caller can
// present a specific, actionable message instead of an opaque error string.
type FailureReason string

// ReasonDependencyNotVendored means the chart declares (in Chart.yaml) one or
// more dependencies with no corresponding vendored charts/*.tgz artifact —
// Helm would need to pull them from a registry, which Render never does.
const ReasonDependencyNotVendored FailureReason = "dependency-not-vendored"

// Failure is a classified render failure. It implements error so callers that
// don't care about the classification can still treat it as one, while
// callers that do can errors.As(err, &chartrender.Failure{}) to branch on
// Reason.
type Failure struct {
	Reason FailureReason
	// ChartDir is the chart directory that was being rendered.
	ChartDir string
	// Missing lists the declared dependency names with no vendored artifact.
	Missing []string
}

func (f *Failure) Error() string {
	return fmt.Sprintf("chartrender: %s: chart %q is missing vendored dependencies: %v", f.Reason, f.ChartDir, f.Missing)
}

// Render renders the chart rooted at chartDir — a directory containing
// Chart.yaml, values.yaml, and any vendored charts/*.tgz — to Kubernetes
// manifests. Rendering is always client-only (no cluster contact) and fully
// offline (no registry pull). values is merged over the chart's own
// values.yaml; nil renders with defaults only.
//
// If chartDir declares a dependency with no vendored artifact, Render returns
// a *Failure with Reason == ReasonDependencyNotVendored.
func Render(chartDir string, values map[string]interface{}) (*Result, error) {
	chrt, err := loader.LoadDir(chartDir)
	if err != nil {
		return nil, fmt.Errorf("chartrender: load chart at %q: %w", chartDir, err)
	}

	if missing := missingDependencies(chrt); len(missing) > 0 {
		return nil, &Failure{Reason: ReasonDependencyNotVendored, ChartDir: chartDir, Missing: missing}
	}

	install := action.NewInstall(&action.Configuration{Log: func(string, ...interface{}) {}})
	install.ClientOnly = true
	install.DryRun = true
	install.ReleaseName = "chartrender-spike"
	install.Namespace = "default"
	install.Replace = true

	rel, err := install.Run(chrt, values)
	if err != nil {
		return nil, fmt.Errorf("chartrender: render chart at %q: %w", chartDir, err)
	}

	return &Result{Manifest: rel.Manifest}, nil
}

// missingDependencies returns the names of dependencies declared in
// Chart.yaml that have no corresponding loaded (vendored) subchart.
func missingDependencies(chrt *chart.Chart) []string {
	loaded := make(map[string]bool, len(chrt.Dependencies()))
	for _, d := range chrt.Dependencies() {
		loaded[d.Name()] = true
	}

	var missing []string
	for _, dep := range chrt.Metadata.Dependencies {
		if !loaded[dep.Name] {
			missing = append(missing, dep.Name)
		}
	}
	return missing
}
