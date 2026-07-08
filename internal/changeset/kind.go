package changeset

import (
	"path/filepath"
	"strings"
)

// Kind classifies a Change by the role of the file it came from.
type Kind string

const (
	// KindChart is a Change sourced from a Chart.yaml file.
	KindChart Kind = "chart"
	// KindValue is a Change sourced from any file other than Chart.yaml or
	// a Terraform/OpenTofu source file (e.g. values.yaml).
	KindValue Kind = "value"

	// KindProvider is a Terraform Change sourced from a provider version
	// constraint (a `required_providers` entry, typically in versions.tf or
	// providers.tf) or a provider pin in .terraform.lock.hcl.
	KindProvider Kind = "provider"
	// KindModule is a Terraform Change sourced from a `module` block's
	// source/version.
	KindModule Kind = "module"
	// KindResource is a Terraform Change sourced from a `resource` block's
	// attribute — the default Kind for a .tf/.tofu file that isn't
	// recognized as provider/module/variable.
	KindResource Kind = "resource"
	// KindVariable is a Terraform Change sourced from a `variable` block.
	KindVariable Kind = "variable"
)

// terraformLockfileName is the fixed basename Terraform/OpenTofu always use
// for the dependency lock file — never a glob, always this exact name.
const terraformLockfileName = ".terraform.lock.hcl"

// IsTerraform reports whether k is one of the Terraform/OpenTofu-sourced
// Kinds (provider/module/resource/variable) -- the routing basis for the
// plandiff resource-change view (acceptance criterion 8): a Terraform
// changeset is one whose Changeset contains at least one Change whose Kind
// satisfies IsTerraform.
func (k Kind) IsTerraform() bool {
	switch k {
	case KindProvider, KindModule, KindResource, KindVariable:
		return true
	default:
		return false
	}
}

// ClassifyKind classifies a Change by the basename of its source file path.
// Chart.yaml yields KindChart; a Terraform/OpenTofu source
// (.tf/.tofu/.terraform.lock.hcl) is classified by classifyTerraformKind;
// every other basename (e.g. values.yaml) yields KindValue.
func ClassifyKind(filePath string) Kind {
	base := filepath.Base(filePath)
	switch {
	case base == "Chart.yaml":
		return KindChart
	case isTerraformSource(base):
		return classifyTerraformKind(base)
	default:
		return KindValue
	}
}

// isTerraformSource reports whether base is a Terraform/OpenTofu source file
// by its conventional suffix/name: .tf, .tofu, or the fixed lockfile name.
func isTerraformSource(base string) bool {
	return base == terraformLockfileName ||
		strings.HasSuffix(base, ".tf") ||
		strings.HasSuffix(base, ".tofu")
}

// classifyTerraformKind classifies a Terraform/OpenTofu source file's
// basename into provider/module/resource/variable, using the file-naming
// convention Terraform projects follow (versions.tf/providers.tf for
// provider constraints, modules.tf for module blocks, variables.tf for
// variable blocks, and everything else defaulting to resource — the
// overwhelmingly common case, matching the dogfood repo's layout, e.g.
// oci-containerengine-nodepool.tf, oci-security-list-worker-nodes.tf).
// The lockfile is always provider — it carries nothing but provider pins.
func classifyTerraformKind(base string) Kind {
	lower := strings.ToLower(base)
	switch {
	case lower == terraformLockfileName:
		return KindProvider
	case strings.Contains(lower, "variable"):
		return KindVariable
	case strings.Contains(lower, "module"):
		return KindModule
	case strings.Contains(lower, "provider"), strings.Contains(lower, "version"):
		return KindProvider
	default:
		return KindResource
	}
}
