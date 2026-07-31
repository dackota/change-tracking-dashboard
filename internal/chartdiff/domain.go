package chartdiff

import (
	"context"
	"errors"

	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// chartDomain is chartdiff's subtree.Domain: everything about a Chart diff
// that is not the shared materialize/cache/bounds orchestration. That is
// three things — render a materialized chart directory, diff two rendered
// manifest sets, and translate an engine Failure into this package's own
// Outcome Kind vocabulary.
//
// Classify is where Unavailable lives, and that is deliberate: an unvendored
// chart dependency is a fact about the Helm renderer, not about the engine,
// so the engine never needs to know chartrender's error type exists.
type chartDomain struct {
	renderer        Renderer
	maxUnifiedBytes int
}

// Produce renders chartDir with the chart's own committed values and maps the
// result onto manifestdiff's independent Manifest type, so manifestdiff never
// needs to import chartrender (the heavy Helm SDK dependency stays contained
// to chartrender).
func (d chartDomain) Produce(chartDir string) ([]manifestdiff.Manifest, error) {
	result, err := d.renderer.Render(chartDir, nil)
	if err != nil {
		return nil, err
	}
	return toManifestdiffManifests(result), nil
}

// Combine diffs both sides' rendered manifests into the OK Outcome.
func (d chartDomain) Combine(old, new []manifestdiff.Manifest) Outcome {
	return Outcome{Kind: OK, Diff: manifestdiff.Diff(manifestdiff.Params{
		Old:             old,
		New:             new,
		MaxUnifiedBytes: d.maxUnifiedBytes,
	})}
}

// Classify maps an engine Failure onto this package's Kind vocabulary:
//
//   - NoPriorVersion (the commit is a root commit) -> NoPriorVersion.
//   - ExceededLimits (a materialize bound, or a materialize/render timeout)
//     -> ExceededLimits.
//   - ProduceFailed carrying chartrender's ReasonDependencyNotVendored ->
//     Unavailable; ReasonMalformedChart -> CouldNotRender.
//   - anything else -> CouldNotRender, the safe generic bucket. The specific
//     cause is logged server-side here and never attached to the Outcome.
//
// The engine has already logged every Kind other than ProduceFailed, so only
// an unclassified render failure is logged below — a classified one is an
// expected outcome, not an error.
func (d chartDomain) Classify(ctx context.Context, f subtree.Failure) Outcome {
	switch f.Kind {
	case subtree.NoPriorVersion:
		return Outcome{Kind: NoPriorVersion}
	case subtree.ExceededLimits:
		return Outcome{Kind: ExceededLimits}
	case subtree.ProduceFailed:
		var failure *chartrender.Failure
		if errors.As(f.Cause, &failure) {
			switch failure.Reason {
			case chartrender.ReasonDependencyNotVendored:
				return Outcome{Kind: Unavailable}
			case chartrender.ReasonMalformedChart:
				return Outcome{Kind: CouldNotRender}
			}
		}
		telemetry.LoggerFromContext(ctx).Error("chartdiff: render failed",
			"side", f.Side, "repo", f.Req.RepoName, "tenant", f.Req.TenantPath, "sha", f.Sha, "error", f.Cause)
		return Outcome{Kind: CouldNotRender}
	}
	return Outcome{Kind: CouldNotRender}
}
