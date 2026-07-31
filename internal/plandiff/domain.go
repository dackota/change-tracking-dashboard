package plandiff

import (
	"context"
	"errors"

	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
	"github.com/dackota/change-tracking-dashboard/internal/subtree"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// planDomain is plandiff's subtree.Domain: everything about a static
// plan-diff that is not the shared materialize/cache/bounds orchestration.
// That is three things — parse a materialized Terraform stack directory,
// classify the resource-level delta between two parsed sides, and translate
// an engine Failure into this package's own Outcome Kind vocabulary.
//
// Classify is where errBlockDepthExceeded is recognised, and that is
// deliberate: nested-block recursion depth is a fact about this package's HCL
// parser, not about the engine, so the engine never needs to know the
// sentinel exists.
type planDomain struct {
	parser          Parser
	maxUnifiedBytes int
	forceAttrs      map[string]struct{}
}

// Produce parses dir into the resource set both sides of the diff are built
// from. It never executes `terraform plan` or `terraform show -json` and
// never touches cloud credentials or state — its only inputs are the HCL
// bytes the materialize step already wrote to local disk.
func (d planDomain) Produce(dir string) ([]Resource, error) {
	return d.parser.Parse(dir)
}

// Combine classifies the resource-level delta (added/removed/changed, with
// the replacement-forcing heuristic) and renders both sides through
// manifestdiff into the OK Outcome.
func (d planDomain) Combine(old, new []Resource) Outcome {
	deltas, summary := resourceDelta(old, new, d.forceAttrs)

	diff := manifestdiff.Diff(manifestdiff.Params{
		Old:             toManifestdiffManifests(old),
		New:             toManifestdiffManifests(new),
		MaxUnifiedBytes: d.maxUnifiedBytes,
	})

	return Outcome{Kind: OK, Diff: diff, Summary: summary, Resources: deltas}
}

// Classify maps an engine Failure onto this package's Kind vocabulary:
//
//   - NoPriorVersion (the commit is a root commit) -> NoPriorVersion.
//   - ExceededLimits (a materialize bound, or a materialize/parse timeout)
//     -> ExceededLimits.
//   - ProduceFailed carrying errBlockDepthExceeded (a resource body nested
//     deeper than Config.MaxBlockDepth) -> ExceededLimits, mirroring how the
//     engine already classifies gitsource.ErrMaterializeBoundsExceeded.
//   - anything else -> CouldNotRender, the safe generic bucket. The specific
//     cause is logged server-side here and never attached to the Outcome.
//
// The engine has already logged every Kind other than ProduceFailed, so only
// an unclassified parse failure is logged below — a bound that tripped is an
// expected outcome, not an error.
func (d planDomain) Classify(ctx context.Context, f subtree.Failure) Outcome {
	switch f.Kind {
	case subtree.NoPriorVersion:
		return Outcome{Kind: NoPriorVersion}
	case subtree.ExceededLimits:
		return Outcome{Kind: ExceededLimits}
	case subtree.ProduceFailed:
		if errors.Is(f.Cause, errBlockDepthExceeded) {
			return Outcome{Kind: ExceededLimits}
		}
		telemetry.LoggerFromContext(ctx).Error("plandiff: parse failed",
			"side", f.Side, "repo", f.Req.RepoName, "tenant", f.Req.TenantPath, "sha", f.Sha, "error", f.Cause)
		return Outcome{Kind: CouldNotRender}
	}
	return Outcome{Kind: CouldNotRender}
}
