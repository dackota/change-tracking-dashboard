package plandiff

import "github.com/dackota/change-tracking-dashboard/internal/manifestdiff"

// toManifestdiffManifests maps a Resource set onto manifestdiff's
// independent Manifest type, so manifestdiff never needs to know anything
// about Terraform or HCL: ResourceType -> Kind, "" -> Namespace (Terraform
// resource addresses have no namespace concept), ResourceName -> Name, and
// Body -> YAML (an opaque, deterministically-rendered text block --
// manifestdiff treats it exactly like it treats a chart's manifest YAML: as
// untrusted text to line-diff, never parsed or interpreted). Mirrors
// chartdiff.toManifestdiffManifests's identical mapping role.
func toManifestdiffManifests(resources []Resource) []manifestdiff.Manifest {
	out := make([]manifestdiff.Manifest, len(resources))
	for i, r := range resources {
		out[i] = manifestdiff.Manifest{
			Kind: r.Type,
			Name: r.Name,
			YAML: r.Body,
		}
	}
	return out
}
