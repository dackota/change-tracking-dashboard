package plandiff_test

import (
	"context"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// findDelta returns the ResourceDelta for name in deltas, failing the test if
// absent.
func findDelta(t *testing.T, deltas []plandiff.ResourceDelta, name string) plandiff.ResourceDelta {
	t.Helper()
	for _, d := range deltas {
		if d.ResourceName == name {
			return d
		}
	}
	t.Fatalf("no ResourceDelta for %q in %+v", name, deltas)
	return plandiff.ResourceDelta{}
}

// TestDiff_ForceReplacementAttributeChange_FlagsReplacement proves acceptance
// criterion 2: a change to a configured replacement-forcing attribute
// (Config.ForceReplacementAttrs) flags that resource's delta as
// ForcesReplacement, while an unrelated attribute change on another resource
// does not.
func TestDiff_ForceReplacementAttributeChange_FlagsReplacement(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	parser := sequentialResourceParser(
		[]plandiff.Resource{
			{Type: "oci_core_instance", Name: "moved", Attrs: map[string]string{"availability_domain": "AD-1"}, Body: "availability_domain = \"AD-1\"\n"},
			{Type: "oci_core_instance", Name: "resized", Attrs: map[string]string{"shape": "small"}, Body: "shape = \"small\"\n"},
		},
		[]plandiff.Resource{
			{Type: "oci_core_instance", Name: "moved", Attrs: map[string]string{"availability_domain": "AD-2"}, Body: "availability_domain = \"AD-2\"\n"},
			{Type: "oci_core_instance", Name: "resized", Attrs: map[string]string{"shape": "large"}, Body: "shape = \"large\"\n"},
		},
	)

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	if outcome.Kind != plandiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.OK)
	}

	moved := findDelta(t, outcome.Resources, "moved")
	if !moved.ForcesReplacement {
		t.Errorf("resource 'moved' (availability_domain changed) ForcesReplacement = false, want true")
	}

	resized := findDelta(t, outcome.Resources, "resized")
	if resized.ForcesReplacement {
		t.Errorf("resource 'resized' (only 'shape' changed, not a configured force-replacement attr) ForcesReplacement = true, want false")
	}

	if outcome.Summary.Replaced != 1 {
		t.Errorf("Summary.Replaced = %d, want 1", outcome.Summary.Replaced)
	}
}

// TestDiff_RemovedResource_AlwaysFlagsReplacement proves acceptance
// criterion 2 for removal: a resource that disappears entirely is always
// flagged ForcesReplacement (PRD R13: removing a resource is itself
// destructive), regardless of ForceReplacementAttrs configuration.
func TestDiff_RemovedResource_AlwaysFlagsReplacement(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	parser := sequentialResourceParser(
		[]plandiff.Resource{
			{Type: "oci_core_instance", Name: "gone", Attrs: map[string]string{"shape": "x"}, Body: "shape = \"x\"\n"},
		},
		nil,
	)

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	gone := findDelta(t, outcome.Resources, "gone")
	if gone.Kind != plandiff.ResourceRemoved {
		t.Fatalf("Kind = %q, want %q", gone.Kind, plandiff.ResourceRemoved)
	}
	if !gone.ForcesReplacement {
		t.Errorf("removed resource ForcesReplacement = false, want true")
	}
}

// TestDiff_AddedResource_NeverFlagsReplacement proves a brand-new resource
// never flags ForcesReplacement -- nothing is being replaced.
func TestDiff_AddedResource_NeverFlagsReplacement(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	parser := sequentialResourceParser(
		nil,
		[]plandiff.Resource{
			{Type: "oci_core_instance", Name: "brand-new", Attrs: map[string]string{"availability_domain": "AD-1"}, Body: "availability_domain = \"AD-1\"\n"},
		},
	)

	engine, err := plandiff.NewEngine(plandiff.Config{}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	added := findDelta(t, outcome.Resources, "brand-new")
	if added.Kind != plandiff.ResourceAdded {
		t.Fatalf("Kind = %q, want %q", added.Kind, plandiff.ResourceAdded)
	}
	if added.ForcesReplacement {
		t.Errorf("added resource ForcesReplacement = true, want false")
	}
}

// TestDiff_CustomForceReplacementAttrs_OverridesDefault proves
// Config.ForceReplacementAttrs is honored: an attribute outside the default
// list flags replacement when explicitly configured, and the default list's
// own attributes stop flagging when overridden away from them.
func TestDiff_CustomForceReplacementAttrs_OverridesDefault(t *testing.T) {
	t.Parallel()

	repo := fixedParentRepo("parent-sha")
	parser := sequentialResourceParser(
		[]plandiff.Resource{
			{Type: "oci_core_instance", Name: "web", Attrs: map[string]string{"custom_immutable": "a", "availability_domain": "AD-1"}, Body: "x\n"},
		},
		[]plandiff.Resource{
			{Type: "oci_core_instance", Name: "web", Attrs: map[string]string{"custom_immutable": "b", "availability_domain": "AD-2"}, Body: "y\n"},
		},
	)

	engine, err := plandiff.NewEngine(plandiff.Config{ForceReplacementAttrs: []string{"custom_immutable"}}, parser)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "sha"})

	web := findDelta(t, outcome.Resources, "web")
	if !web.ForcesReplacement {
		t.Errorf("ForcesReplacement = false, want true (custom_immutable changed and is configured)")
	}
}
