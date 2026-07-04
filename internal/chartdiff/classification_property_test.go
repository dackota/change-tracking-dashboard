package chartdiff_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartdiff"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/chartrender"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
)

// validOutcomeKinds is the fixed, exhaustive set of Kinds Diff may ever
// return — the domain the classification-totality property asserts against.
var validOutcomeKinds = map[chartdiff.Kind]bool{
	chartdiff.OK:             true,
	chartdiff.NoPriorVersion: true,
	chartdiff.Unavailable:    true,
	chartdiff.CouldNotRender: true,
	chartdiff.ExceededLimits: true,
}

// materializeFailOnCall returns a fakeChartRepo materialize func that fails
// with err on exactly the callth invocation (1-indexed: 1 = old side,
// 2 = new side) and succeeds otherwise — used to place a failure on a
// specific side of the diff.
func materializeFailOnCall(call int, err error) func(string, string, string, gitsource.MaterializeBounds) error {
	var n int32
	return func(string, string, string, gitsource.MaterializeBounds) error {
		if int(atomic.AddInt32(&n, 1)) == call {
			return err
		}
		return nil
	}
}

// renderFailOnCall returns a fakeRenderer func that fails with err on
// exactly the callth invocation (1-indexed) and succeeds with a distinct
// manifest otherwise.
func renderFailOnCall(call int, err error) func(int, string, map[string]interface{}) (*chartrender.Result, error) {
	return func(callN int, _ string, _ map[string]interface{}) (*chartrender.Result, error) {
		if callN == call {
			return nil, err
		}
		return &chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: fmt.Sprintf("m%d", callN), YAML: "v: 1\n"}}}, nil
	}
}

// TestDiff_ClassificationTotality_Property asserts the invariant that must
// hold for every combination of parent-resolution result and
// materialize/render result on either side of the diff: Diff returns exactly
// one of the fixed, defined Outcome Kinds, and never panics. The
// (parent-resolution x materialize-result x render-result x which-side)
// space is small and finite (unlike the byte/path spaces elsewhere in this
// slice), so it is asserted exhaustively here rather than via randomized
// generation — every cell of the matrix is a scenario below.
func TestDiff_ClassificationTotality_Property(t *testing.T) {
	t.Parallel()

	fixedParent := func(string) (string, error) { return "parent-sha", nil }
	okRender := func(callN int, _ string, _ map[string]interface{}) (*chartrender.Result, error) {
		return &chartrender.Result{Manifests: []chartrender.Manifest{{Kind: "ConfigMap", Name: fmt.Sprintf("m%d", callN), YAML: "v: 1\n"}}}, nil
	}

	scenarios := []struct {
		name        string
		firstParent func(string) (string, error)
		materialize func(string, string, string, gitsource.MaterializeBounds) error
		render      func(int, string, map[string]interface{}) (*chartrender.Result, error)
	}{
		{"root commit -> no parent to diff", func(string) (string, error) { return "", gitsource.ErrNoParent }, nil, okRender},
		{"first-parent unclassified error", func(string) (string, error) { return "", errUnexpected }, nil, okRender},
		{"materialize bounds exceeded (old side)", fixedParent, materializeFailOnCall(1, gitsource.ErrMaterializeBoundsExceeded), okRender},
		{"materialize bounds exceeded (new side)", fixedParent, materializeFailOnCall(2, gitsource.ErrMaterializeBoundsExceeded), okRender},
		{"materialize unclassified error (old side)", fixedParent, materializeFailOnCall(1, errUnexpected), okRender},
		{"render dependency-not-vendored (old side)", fixedParent, nil, renderFailOnCall(1, &chartrender.Failure{Reason: chartrender.ReasonDependencyNotVendored})},
		{"render dependency-not-vendored (new side)", fixedParent, nil, renderFailOnCall(2, &chartrender.Failure{Reason: chartrender.ReasonDependencyNotVendored})},
		{"render malformed chart (old side)", fixedParent, nil, renderFailOnCall(1, &chartrender.Failure{Reason: chartrender.ReasonMalformedChart})},
		{"render malformed chart (new side)", fixedParent, nil, renderFailOnCall(2, &chartrender.Failure{Reason: chartrender.ReasonMalformedChart})},
		{"render unclassified error (old side)", fixedParent, nil, renderFailOnCall(1, errUnexpected)},
		{"both sides render successfully", fixedParent, nil, okRender},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()

			repo := &fakeChartRepo{firstParentFn: sc.firstParent, materializeFn: sc.materialize}
			renderer := &fakeRenderer{fn: sc.render}

			engine, err := chartdiff.NewEngine(chartdiff.Config{}, renderer)
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}

			outcome := engine.Diff(context.Background(), repo, chartdiff.Request{RepoName: "r", TenantPath: "tenant", CommitSha: "sha"})

			if !validOutcomeKinds[outcome.Kind] {
				t.Errorf("outcome.Kind = %q, want one of the defined Kinds", outcome.Kind)
			}
		})
	}
}
