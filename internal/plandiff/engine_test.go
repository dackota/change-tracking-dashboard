package plandiff_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// TestDiff_SuccessPath_ReturnsResourceLevelDelta proves acceptance criterion
// 1: diffing a commit against its parent returns a resource-level delta of
// resources added, removed, and attribute-changed, rendered through
// manifestdiff.
func TestDiff_SuccessPath_ReturnsResourceLevelDelta(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	parser := sequentialResourceParser(
		[]plandiff.Resource{
			{Type: "oci_core_instance", Name: "web", Attrs: map[string]string{"shape": "VM.Standard.E4.Flex"}, Body: "shape = \"VM.Standard.E4.Flex\"\n"},
			{Type: "oci_core_instance", Name: "stale", Attrs: map[string]string{"shape": "VM.Standard.E4.Flex"}, Body: "shape = \"VM.Standard.E4.Flex\"\n"},
		},
		[]plandiff.Resource{
			{Type: "oci_core_instance", Name: "web", Attrs: map[string]string{"shape": "VM.Standard.E4.Flex.Big"}, Body: "shape = \"VM.Standard.E4.Flex.Big\"\n"},
			{Type: "oci_core_instance", Name: "new", Attrs: map[string]string{"shape": "VM.Standard.E4.Flex"}, Body: "shape = \"VM.Standard.E4.Flex\"\n"},
		},
	)

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{
		RepoName:   "infra",
		TenantPath: "envs/prod",
		CommitSha:  "commit-sha",
	})

	if outcome.Kind != plandiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.OK)
	}
	if outcome.Summary.Added != 1 {
		t.Errorf("Summary.Added = %d, want 1", outcome.Summary.Added)
	}
	if outcome.Summary.Removed != 1 {
		t.Errorf("Summary.Removed = %d, want 1", outcome.Summary.Removed)
	}
	if outcome.Summary.Changed != 1 {
		t.Errorf("Summary.Changed = %d, want 1", outcome.Summary.Changed)
	}
	if len(outcome.Resources) != 3 {
		t.Fatalf("len(Resources) = %d, want 3", len(outcome.Resources))
	}

	byName := make(map[string]plandiff.ResourceDelta, len(outcome.Resources))
	for _, r := range outcome.Resources {
		byName[r.ResourceName] = r
	}
	if got := byName["new"].Kind; got != plandiff.ResourceAdded {
		t.Errorf("resource 'new' Kind = %q, want %q", got, plandiff.ResourceAdded)
	}
	if got := byName["stale"].Kind; got != plandiff.ResourceRemoved {
		t.Errorf("resource 'stale' Kind = %q, want %q", got, plandiff.ResourceRemoved)
	}
	if got := byName["web"].Kind; got != plandiff.ResourceChanged {
		t.Errorf("resource 'web' Kind = %q, want %q", got, plandiff.ResourceChanged)
	}

	if !strings.Contains(outcome.Diff.Unified, "-shape") || !strings.Contains(outcome.Diff.Unified, "+shape") {
		t.Errorf("Unified diff missing expected +/- attribute lines:\n%s", outcome.Diff.Unified)
	}
}

// TestDiff_IdenticalResourceSets_ReturnsEmptyDelta proves the diff produces
// no spurious change when both sides are identical.
func TestDiff_IdenticalResourceSets_ReturnsEmptyDelta(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	resources := []plandiff.Resource{
		{Type: "oci_core_instance", Name: "web", Attrs: map[string]string{"shape": "x"}, Body: "shape = \"x\"\n"},
	}
	parser := sequentialResourceParser(resources, resources)

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	if outcome.Kind != plandiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.OK)
	}
	if len(outcome.Resources) != 0 {
		t.Errorf("Resources = %+v, want empty (identical resource sets)", outcome.Resources)
	}
	if outcome.Summary != (plandiff.Summary{}) {
		t.Errorf("Summary = %+v, want zero value", outcome.Summary)
	}
}
