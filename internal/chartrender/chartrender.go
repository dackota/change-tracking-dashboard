// Package chartrender renders a Helm chart to Kubernetes manifests in-process
// via the embedded Helm v4 SDK. It is the seam that encapsulates ALL Helm v4
// SDK usage (see ADR 0002): render is always client-only and fully offline —
// no Kubernetes API contact, no chart-registry pull. The chart must be fully
// vendored: every dependency declared in Chart.yaml must have a corresponding
// committed charts/*.tgz (or charts/<name>/ directory) artifact. Render never
// returns a partial result or an opaque Helm error: a chart that cannot be
// rendered — whether from an unvendored dependency or malformed chart
// content — returns a classified *Failure instead.
//
// Render's output is a normalized manifest set (see normalize.go), not raw
// Helm output: split into individual documents, sorted by
// (Kind, Namespace, Name), and re-serialized as canonical YAML. That
// determinism is what lets the downstream manifestdiff module line-diff two
// renders without noise from Helm's nondeterministic document/key ordering.
package chartrender

import (
	"fmt"
	"log/slog"

	"helm.sh/helm/v4/pkg/action"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/release"
)

// FailureReason classifies why a chart could not be rendered, so a caller can
// present a specific, actionable message instead of an opaque error string.
type FailureReason string

const (
	// ReasonDependencyNotVendored means the chart declares (in Chart.yaml)
	// one or more dependencies with no corresponding vendored charts/*.tgz
	// artifact — Helm would need to pull them from a registry, which Render
	// never does.
	ReasonDependencyNotVendored FailureReason = "dependency-not-vendored"
	// ReasonMalformedChart means the chart could not be loaded (missing or
	// unparseable Chart.yaml, unparseable templates) or failed to render for
	// a chart-content reason (template syntax error, a template producing
	// invalid YAML, and similar). Render is client-only and offline, so once
	// the dependency-vendoring check above has passed, a render failure can
	// only be caused by the chart's own content, never by cluster or network
	// conditions.
	ReasonMalformedChart FailureReason = "malformed-chart"
)

// Failure is a classified render failure. It implements error so callers that
// don't care about the classification can still treat it as one, while
// callers that do can errors.As(err, &chartrender.Failure{}) to branch on
// Reason.
//
// Cause, when set, is the underlying Helm/loader error and is folded into
// Error() for server-side logging. A caller building a user-facing (e.g. web)
// message should key off Reason and ChartDir alone, without calling Error()
// or inspecting Cause, so Helm's internal error text never reaches an
// end user.
type Failure struct {
	Reason FailureReason
	// ChartDir is the chart directory that was being rendered.
	ChartDir string
	// Missing lists the declared dependency names with no vendored artifact.
	// Only set when Reason == ReasonDependencyNotVendored.
	Missing []string
	// Cause is the underlying error, retained for server-side logging. Only
	// set when Reason == ReasonMalformedChart.
	Cause error
}

func (f *Failure) Error() string {
	if f.Reason == ReasonDependencyNotVendored {
		return fmt.Sprintf("chartrender: %s: chart %q is missing vendored dependencies: %v", f.Reason, f.ChartDir, f.Missing)
	}
	return fmt.Sprintf("chartrender: %s: chart %q: %v", f.Reason, f.ChartDir, f.Cause)
}

// Unwrap exposes Cause so errors.Is/errors.As can see through a Failure to
// the underlying Helm/loader error, without that error being part of the
// generic, user-facing surface (Reason/ChartDir).
func (f *Failure) Unwrap() error { return f.Cause }

// Render renders the chart rooted at chartDir — a directory containing
// Chart.yaml, values.yaml, and any vendored charts/*.tgz — to Kubernetes
// manifests. Rendering is always client-only (no cluster contact) and fully
// offline (no registry pull). values is merged over the chart's own
// values.yaml; nil renders with defaults only.
//
// If chartDir declares a dependency with no vendored artifact, Render returns
// a *Failure with Reason == ReasonDependencyNotVendored. If the chart cannot
// be loaded or fails to render for any other (necessarily chart-content)
// reason, Render returns a *Failure with Reason == ReasonMalformedChart.
func Render(chartDir string, values map[string]interface{}) (*Result, error) {
	chrt, err := loader.LoadDir(chartDir)
	if err != nil {
		return nil, &Failure{Reason: ReasonMalformedChart, ChartDir: chartDir, Cause: fmt.Errorf("load chart: %w", err)}
	}

	if missing := missingDependencies(chrt); len(missing) > 0 {
		return nil, &Failure{Reason: ReasonDependencyNotVendored, ChartDir: chartDir, Missing: missing}
	}

	// Helm v4 replaced v3's separate ClientOnly+DryRun booleans with a single
	// DryRunStrategy; DryRunClient is the "render locally, never touch a
	// cluster or registry" mode this seam requires. The logger is discarded so
	// Helm's internal debug output never reaches our process's stderr (v4's
	// Configuration dropped v3's per-call Log func for an embedded slog logger).
	cfg := action.NewConfiguration(action.ConfigurationSetLogger(slog.DiscardHandler))
	install := action.NewInstall(cfg)
	install.DryRunStrategy = action.DryRunClient
	install.ReleaseName = "chartrender"
	install.Namespace = "default"
	install.Replace = true

	rel, err := install.Run(chrt, values)
	if err != nil {
		return nil, &Failure{Reason: ReasonMalformedChart, ChartDir: chartDir, Cause: fmt.Errorf("render chart: %w", err)}
	}

	// v4's Install.Run returns a release.Releaser (an `any`); the rendered
	// manifest is read through the release accessor rather than v3's direct
	// rel.Manifest struct field.
	rac, err := release.NewAccessor(rel)
	if err != nil {
		return nil, &Failure{Reason: ReasonMalformedChart, ChartDir: chartDir, Cause: fmt.Errorf("read rendered release: %w", err)}
	}

	manifests, err := normalize(rac.Manifest())
	if err != nil {
		return nil, &Failure{Reason: ReasonMalformedChart, ChartDir: chartDir, Cause: err}
	}

	return &Result{Manifests: manifests}, nil
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
