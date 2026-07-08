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
	// KindTerraform is a Change sourced from a Terraform/OpenTofu source
	// file (.tf or .tofu) -- the routing basis for the plandiff
	// resource-change view (acceptance criterion 8): a Terraform changeset
	// is one whose Changeset contains at least one KindTerraform Change.
	// Deliberately excludes .terraform.lock.hcl (a provider-pin lockfile
	// with no resource blocks to diff at the resource level -- its Changes
	// remain KindValue, rendered as a plain old->new value delta, exactly
	// as before this Kind existed).
	KindTerraform Kind = "terraform"
	// KindValue is a Change sourced from any file other than Chart.yaml or
	// a Terraform/OpenTofu source file (e.g. values.yaml,
	// .terraform.lock.hcl).
	KindValue Kind = "value"
)

// terraformSourceSuffixes are the file-extension suffixes that classify a
// Change as KindTerraform. Mirrors extractor.hclGlobSuffixes' file-shape
// convention, deliberately narrower: that list also includes
// ".terraform.lock.hcl" (routing a *tracker* to the HCL FieldExtractor
// engine), but a lockfile has no resource blocks for plandiff to diff, so
// it is excluded here -- see KindTerraform's doc.
var terraformSourceSuffixes = []string{".tf", ".tofu"}

// ClassifyKind classifies a Change by its source file path. Chart.yaml
// (by basename) yields KindChart; a .tf or .tofu file (by extension) yields
// KindTerraform; every other file (e.g. values.yaml, .terraform.lock.hcl)
// yields KindValue.
func ClassifyKind(filePath string) Kind {
	if filepath.Base(filePath) == "Chart.yaml" {
		return KindChart
	}
	for _, suffix := range terraformSourceSuffixes {
		if strings.HasSuffix(filePath, suffix) {
			return KindTerraform
		}
	}
	return KindValue
}
