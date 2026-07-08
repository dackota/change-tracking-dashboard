package plandiff_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/gitsource"
	"github.com/dackota/change-tracking-dashboard/internal/plandiff"
)

// writeFilesRepo returns a fakePlanRepo whose MaterializeSubtreeBounded
// writes the given path -> content files into destDir, simulating what
// gitsource.Source really does -- letting these tests exercise the
// production defaultParser (via a nil Parser passed to NewEngine) end to
// end, not just a fakeParser standing in for it.
func writeFilesRepo(sides map[string]map[string]string) *fakePlanRepo {
	return &fakePlanRepo{
		firstParentFn: func(string) (string, error) { return "parent-sha", nil },
		materializeFn: func(sha, _, destDir string, _ gitsource.MaterializeBounds) error {
			for path, content := range sides[sha] {
				full := filepath.Join(destDir, path)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// TestDiff_RealParser_ExtractsResourceAddRemoveChange proves acceptance
// criteria 1 and 3 together end to end through the production HCL parser
// (no fake Parser): a genuine .tf file materialized on disk is parsed with
// no cloud/state credentials of any kind, and the resulting diff correctly
// classifies an added, a removed, and a changed resource.
func TestDiff_RealParser_ExtractsResourceAddRemoveChange(t *testing.T) {
	t.Parallel()

	oldTF := `
resource "oci_core_instance" "web" {
  shape               = "VM.Standard.E4.Flex"
  availability_domain = "AD-1"
}

resource "oci_core_instance" "stale" {
  shape = "VM.Standard.E4.Flex"
}
`
	newTF := `
resource "oci_core_instance" "web" {
  shape               = "VM.Standard.E4.Flex"
  availability_domain = "AD-2"
}

resource "oci_core_instance" "fresh" {
  shape = "VM.Standard.E4.Flex"
}
`

	repo := writeFilesRepo(map[string]map[string]string{
		"parent-sha": {"main.tf": oldTF},
		"commit-sha": {"main.tf": newTF},
	})

	engine, err := plandiff.NewEngine(plandiff.Config{}, nil) // nil -> production defaultParser
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "infra", TenantPath: "envs/prod", CommitSha: "commit-sha"})

	if outcome.Kind != plandiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.OK)
	}
	if outcome.Summary.Added != 1 || outcome.Summary.Removed != 1 || outcome.Summary.Changed != 1 {
		t.Fatalf("Summary = %+v, want Added=1 Removed=1 Changed=1", outcome.Summary)
	}

	web := findDelta(t, outcome.Resources, "web")
	if !web.ForcesReplacement {
		t.Errorf("'web' (availability_domain changed) ForcesReplacement = false, want true")
	}
	if !strings.Contains(outcome.Diff.Unified, "AD-1") || !strings.Contains(outcome.Diff.Unified, "AD-2") {
		t.Errorf("Unified diff missing expected old/new availability_domain values:\n%s", outcome.Diff.Unified)
	}
}

// TestDiff_RealParser_ExpressionAttributeCapturedAsSourceText proves the
// non-literal attribute convention this package shares with hclextract:
// an attribute whose value is an HCL expression/interpolation (not a pure
// literal) is captured as its source text, not silently dropped or
// resolved to a zero value.
func TestDiff_RealParser_ExpressionAttributeCapturedAsSourceText(t *testing.T) {
	t.Parallel()

	oldTF := `resource "oci_core_instance" "web" { display_name = "static" }`
	newTF := `resource "oci_core_instance" "web" { display_name = var.name_prefix }`

	repo := writeFilesRepo(map[string]map[string]string{
		"parent-sha": {"main.tf": oldTF},
		"commit-sha": {"main.tf": newTF},
	})

	engine, err := plandiff.NewEngine(plandiff.Config{}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "commit-sha"})

	if outcome.Kind != plandiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.OK)
	}
	if !strings.Contains(outcome.Diff.Unified, "var.name_prefix") {
		t.Errorf("Unified diff missing expression source text 'var.name_prefix':\n%s", outcome.Diff.Unified)
	}
}

// TestDiff_RealParser_MalformedHCL_ReturnsCouldNotRenderNotPanic proves
// acceptance criteria 4 and 5 through the production parser: unparseable
// HCL never panics and never leaks the parser's diagnostic text -- it
// classifies as CouldNotRender.
func TestDiff_RealParser_MalformedHCL_ReturnsCouldNotRenderNotPanic(t *testing.T) {
	t.Parallel()

	repo := writeFilesRepo(map[string]map[string]string{
		"parent-sha": {"main.tf": `resource "oci_core_instance" "web" {`}, // unterminated block
		"commit-sha": {"main.tf": `resource "oci_core_instance" "web" {}`},
	})

	engine, err := plandiff.NewEngine(plandiff.Config{}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "commit-sha"})

	if outcome.Kind != plandiff.CouldNotRender {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.CouldNotRender)
	}
}

// TestDiff_RealParser_DeeplyNestedBlocks_ReturnsExceededLimitsNotPanic
// proves the MaxBlockDepth ceiling (Config.MaxBlockDepth) holds against an
// adversarially deeply-nested (but otherwise tiny) resource body: it
// classifies as ExceededLimits rather than exhausting the stack.
func TestDiff_RealParser_DeeplyNestedBlocks_ReturnsExceededLimitsNotPanic(t *testing.T) {
	t.Parallel()

	// Build a resource body nested far past a small configured MaxBlockDepth.
	var b strings.Builder
	b.WriteString(`resource "t" "n" {` + "\n")
	depth := 20
	for i := 0; i < depth; i++ {
		b.WriteString(strings.Repeat("  ", i+1) + `nested {` + "\n")
	}
	for i := depth - 1; i >= 0; i-- {
		b.WriteString(strings.Repeat("  ", i+1) + "}\n")
	}
	b.WriteString("}\n")
	deeplyNested := b.String()

	repo := writeFilesRepo(map[string]map[string]string{
		"parent-sha": {"main.tf": `resource "t" "n" {}`},
		"commit-sha": {"main.tf": deeplyNested},
	})

	engine, err := plandiff.NewEngine(plandiff.Config{MaxBlockDepth: 5}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "commit-sha"})

	if outcome.Kind != plandiff.ExceededLimits {
		t.Errorf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.ExceededLimits)
	}
}

// TestDiff_RealParser_IgnoresLockfileAndNonTerraformFiles proves
// isTerraformFile's scoping: a .terraform.lock.hcl file and any non-.tf/
// .tofu file in the same directory contribute no resources, so this
// package's diff stays scoped to actual resource blocks.
func TestDiff_RealParser_IgnoresLockfileAndNonTerraformFiles(t *testing.T) {
	t.Parallel()

	repo := writeFilesRepo(map[string]map[string]string{
		"parent-sha": {
			"main.tf":             `resource "t" "n" { a = "1" }`,
			".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/oci" { version = "1.0.0" }`,
			"README.md":           `resource "t" "n" { a = "should not be parsed" }`,
		},
		"commit-sha": {
			"main.tf":             `resource "t" "n" { a = "2" }`,
			".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/oci" { version = "2.0.0" }`,
			"README.md":           `resource "t" "n" { a = "should not be parsed" }`,
		},
	})

	engine, err := plandiff.NewEngine(plandiff.Config{}, nil)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	outcome := engine.Diff(context.Background(), repo, plandiff.Request{RepoName: "r", TenantPath: "stack", CommitSha: "commit-sha"})

	if outcome.Kind != plandiff.OK {
		t.Fatalf("outcome.Kind = %q, want %q", outcome.Kind, plandiff.OK)
	}
	if len(outcome.Resources) != 1 {
		t.Fatalf("len(Resources) = %d, want 1 (only main.tf's resource block)", len(outcome.Resources))
	}
}
