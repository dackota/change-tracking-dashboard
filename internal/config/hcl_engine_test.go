package config_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/config"
)

// TestLoad_ExplicitHCLEngine_AcceptsStructuralTraversalExpr verifies a
// tracker with `engine: hcl` and a structural traversal expr (not jq syntax)
// loads successfully — config validation must route the expr through the
// resolved engine's own compiler (extractor.Select), never unconditionally
// through the jq compiler.
func TestLoad_ExplicitHCLEngine_AcceptsStructuralTraversalExpr(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo/tf
    engine: hcl
    facetRegex: ''
    files:
      - glob: 'versions.tf'
        fields:
          - name: required-version
            expr: 'terraform.required_version'
`
	path := writeTemp(t, yaml)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error for a valid hcl traversal expr: %v", err)
	}
	trackers := w.Current().Trackers
	if len(trackers) != 1 {
		t.Fatalf("len(Trackers) = %d, want 1", len(trackers))
	}
	if trackers[0].Engine != "hcl" {
		t.Errorf("Tracker.Engine = %q, want hcl", trackers[0].Engine)
	}
	if trackers[0].ExtractorExpr != "terraform.required_version" {
		t.Errorf("Tracker.ExtractorExpr = %q, want terraform.required_version", trackers[0].ExtractorExpr)
	}
}

// TestLoad_EngineUnset_GlobInfersHCL_AcceptsStructuralTraversalExpr verifies
// that omitting `engine` on a .tf-globbed tracker still validates its expr
// as HCL (via the same glob-based inference the poller uses), not jq — a jq
// compile of a structural traversal expr would often fail or silently
// misparse it.
func TestLoad_EngineUnset_GlobInfersHCL_AcceptsStructuralTraversalExpr(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo/tf
    facetRegex: ''
    files:
      - glob: 'modules.tf'
        fields:
          - name: vpc-module-source
            expr: 'module.vpc.source'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error for a glob-inferred hcl expr: %v", err)
	}
}

// TestLoad_HCLEngine_InvalidTraversalExpr_ReturnsError verifies a malformed
// HCL traversal expression is rejected at load with an actionable error,
// mirroring the existing invalid-jq-expr behavior.
func TestLoad_HCLEngine_InvalidTraversalExpr_ReturnsError(t *testing.T) {
	const yaml = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /repo/tf
    engine: hcl
    facetRegex: ''
    files:
      - glob: 'versions.tf'
        fields:
          - name: bad-path
            expr: 'provider["unterminated'
`
	path := writeTemp(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load should have rejected the malformed hcl traversal expr, got nil")
	}
	if !contains(err.Error(), "hcl") {
		t.Errorf("error %q does not name the hcl engine", err.Error())
	}
}
