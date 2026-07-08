package hclextract_test

import (
	"math/rand"
	"strings"
	"testing"
	"testing/quick"

	"github.com/dackota/change-tracking-dashboard/internal/hclextract"
)

// versionsTF is a representative versions.tf: a terraform block carrying
// required_version and a required_providers block with one provider pin.
const versionsTF = `
terraform {
  required_version = ">= 1.5.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}
`

// modulesTF is a representative module block with both source and version.
const modulesTF = `
module "vpc" {
  source  = "terraform-google-modules/network/google"
  version = "~> 7.0"
}
`

// nodePoolTF is a representative named resource with a nested block
// attribute (a literal) and a top-level attribute set to a traversal
// expression (not a literal).
const nodePoolTF = `
resource "google_container_node_pool" "primary" {
  cluster = google_container_cluster.primary.name

  node_config {
    machine_type = "e2-medium"
  }
}
`

// lockHCL is a representative .terraform.lock.hcl entry. The provider
// address is itself dotted, so it is addressed via bracket-quote syntax
// (see hclextract's path grammar) rather than plain dot segments.
const lockHCL = `
provider "registry.terraform.io/hashicorp/google" {
  version     = "5.10.0"
  constraints = "~> 5.0"
  hashes = [
    "h1:abcdef=",
  ]
}
`

func mustExtractor(t *testing.T, expr string) *hclextract.Extractor {
	t.Helper()
	ex, err := hclextract.New(expr)
	if err != nil {
		t.Fatalf("hclextract.New(%q): unexpected error: %v", expr, err)
	}
	return ex
}

// TestExtract_ProviderVersion proves acceptance criterion 1's extraction
// half: a required_providers entry's version resolves to its literal value.
func TestExtract_ProviderVersion(t *testing.T) {
	t.Parallel()
	ex := mustExtractor(t, "terraform.required_providers.google.version")

	got, err := ex.Extract([]byte(versionsTF))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if !got.Present || got.Value != "~> 5.0" {
		t.Errorf("Extract() = %+v, want Present=true Value=\"~> 5.0\"", got)
	}
}

// TestExtract_RequiredVersion proves acceptance criterion 4: the terraform
// block's required_version attribute resolves to its literal value.
func TestExtract_RequiredVersion(t *testing.T) {
	t.Parallel()
	ex := mustExtractor(t, "terraform.required_version")

	got, err := ex.Extract([]byte(versionsTF))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if !got.Present || got.Value != ">= 1.5.0" {
		t.Errorf("Extract() = %+v, want Present=true Value=\">= 1.5.0\"", got)
	}
}

// TestExtract_ModuleSourceAndVersion proves acceptance criterion 3: both a
// module's source and version attributes resolve to their literal values.
func TestExtract_ModuleSourceAndVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		expr string
		want string
	}{
		{expr: "module.vpc.source", want: "terraform-google-modules/network/google"},
		{expr: "module.vpc.version", want: "~> 7.0"},
	}
	for _, tc := range tests {
		ex := mustExtractor(t, tc.expr)
		got, err := ex.Extract([]byte(modulesTF))
		if err != nil {
			t.Fatalf("Extract(%q): unexpected error: %v", tc.expr, err)
		}
		if !got.Present || got.Value != tc.want {
			t.Errorf("Extract(%q) = %+v, want Present=true Value=%q", tc.expr, got, tc.want)
		}
	}
}

// TestExtract_LockfileProviderPin proves acceptance criterion 2: a
// .terraform.lock.hcl provider block's version pin resolves to its literal
// value, addressed via the bracket-quote path syntax needed because the
// provider address itself contains dots.
func TestExtract_LockfileProviderPin(t *testing.T) {
	t.Parallel()
	ex := mustExtractor(t, `provider["registry.terraform.io/hashicorp/google"].version`)

	got, err := ex.Extract([]byte(lockHCL))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if !got.Present || got.Value != "5.10.0" {
		t.Errorf("Extract() = %+v, want Present=true Value=\"5.10.0\"", got)
	}
}

// TestExtract_NamedResourceAttribute_Literal proves the literal half of
// acceptance criterion 5: a named resource's nested-block attribute resolves
// to its literal value.
func TestExtract_NamedResourceAttribute_Literal(t *testing.T) {
	t.Parallel()
	ex := mustExtractor(t, "resource.google_container_node_pool.primary.node_config.machine_type")

	got, err := ex.Extract([]byte(nodePoolTF))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if !got.Present || got.Value != "e2-medium" {
		t.Errorf("Extract() = %+v, want Present=true Value=\"e2-medium\"", got)
	}
}

// TestExtract_NamedResourceAttribute_Expression proves the expression half
// of acceptance criterion 5: an attribute whose value is an HCL expression
// (here, a traversal reference to another resource, not a literal) is
// captured as its expression source text, not evaluated/resolved.
func TestExtract_NamedResourceAttribute_Expression(t *testing.T) {
	t.Parallel()
	ex := mustExtractor(t, "resource.google_container_node_pool.primary.cluster")

	got, err := ex.Extract([]byte(nodePoolTF))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if !got.Present || got.Value != "google_container_cluster.primary.name" {
		t.Errorf("Extract() = %+v, want Present=true Value=\"google_container_cluster.primary.name\" (raw expression text)", got)
	}
}

// TestExtract_AbsentPath_IsPresentFalse_NotAnError proves acceptance
// criterion 6: a well-formed traversal path that matches nothing in
// well-formed HCL is treated as absent, never an error.
func TestExtract_AbsentPath_IsPresentFalse_NotAnError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		expr string
	}{
		{name: "unknown attribute on an existing block", expr: "resource.google_container_node_pool.primary.node_config.disk_size_gb"},
		{name: "unknown block label", expr: "resource.google_container_node_pool.does_not_exist.node_config.machine_type"},
		{name: "unknown top-level block type", expr: "provider.google.version"},
		{name: "unknown nested key in an object attribute", expr: "terraform.required_providers.aws.version"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ex := mustExtractor(t, tc.expr)
			got, err := ex.Extract([]byte(nodePoolTF + versionsTF))
			if err != nil {
				t.Fatalf("Extract(%q): unexpected error: %v", tc.expr, err)
			}
			if got.Present {
				t.Errorf("Extract(%q) = %+v, want Present=false", tc.expr, got)
			}
		})
	}
}

// TestExtract_EmptyContent_IsPresentFalse mirrors the gojq extractor's
// contract: nil/empty content is absent, not an error.
func TestExtract_EmptyContent_IsPresentFalse(t *testing.T) {
	t.Parallel()
	ex := mustExtractor(t, "terraform.required_version")

	got, err := ex.Extract(nil)
	if err != nil {
		t.Fatalf("Extract(nil): unexpected error: %v", err)
	}
	if got.Present {
		t.Errorf("Extract(nil) = %+v, want Present=false", got)
	}
}

// TestExtract_MalformedHCL_ReturnsClassifiedError proves the extraction half
// of acceptance criterion 8: unparseable HCL is reported as an error (for
// the poller to log/count and skip), never silently treated as absent and
// never a panic.
func TestExtract_MalformedHCL_ReturnsClassifiedError(t *testing.T) {
	t.Parallel()

	malformed := []byte(`resource "google_container_node_pool" "primary" {
  node_config {
    machine_type = "e2-medium"
`) // missing closing braces

	ex := mustExtractor(t, "resource.google_container_node_pool.primary.node_config.machine_type")
	_, err := ex.Extract(malformed)
	if err == nil {
		t.Fatal("Extract(malformed HCL) = nil error, want a parse error")
	}
}

// TestNew_RejectsInvalidExpressions proves the traversal-path parser
// validates its input at construction time rather than failing confusingly
// (or silently) at Extract time.
func TestNew_RejectsInvalidExpressions(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		`provider["unterminated`,
		`provider[not-quoted].version`,
		".",
	}
	for _, expr := range tests {
		if _, err := hclextract.New(expr); err == nil {
			t.Errorf("New(%q) = nil error, want rejection", expr)
		}
	}
}

// TestExtract_NeverPanics_OnAdversarialInput is a property test: for any
// byte slice (empty, truncated, huge, binary garbage, deeply nested), and
// for any syntactically valid traversal path, Extract must never panic — it
// returns either a valid TrackedField or a non-nil error, and never both a
// non-nil error alongside Present=true.
func TestExtract_NeverPanics_OnAdversarialInput(t *testing.T) {
	ex := mustExtractor(t, "resource.a.b.c.d")

	adversarial := [][]byte{
		nil,
		{},
		[]byte("\x00\x01\x02\xff\xfe"),
		[]byte(strings.Repeat("{", 10000)),
		[]byte(strings.Repeat("a.", 5000) + "="),
		[]byte(`resource "a" "b" {`),
		[]byte(`= = = = =`),
		[]byte(strings.Repeat(`resource "a" "b" { c { d = "x" } }`+"\n", 500)),
	}

	for i, content := range adversarial {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("adversarial input %d: Extract panicked: %v", i, r)
				}
			}()
			field, err := ex.Extract(content)
			if err != nil && field.Present {
				t.Errorf("adversarial input %d: got both a non-nil error (%v) and Present=true", i, err)
			}
		}()
	}

	// Randomized fuzzing on top of the hand-picked adversarial corpus above.
	f := func(data []byte) bool {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("random input panicked: %v (input %q)", r, data)
			}
		}()
		_, _ = ex.Extract(data)
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200, Rand: rand.New(rand.NewSource(1))}); err != nil {
		t.Fatalf("quick.Check: %v", err)
	}
}
