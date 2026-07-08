package changeset

import "github.com/dackota/change-tracking-dashboard/internal/domain"

// DefaultRiskRules returns the shipped OCI-dogfood risk rule set (R16):
// resource-type/attribute PATTERNS mapped to a Risk class, not resource
// NAMES baked into the classifier. Every rule below is plain data — adding
// support for a new provider or resource is adding a RiskRule literal here
// (or, for an operator, a config entry with the same shape), never touching
// ClassifyRisk itself.
//
// The patterns are written against the two identifying strings a persisted
// domain.Change actually carries: FilePath (the tracked source file — a
// stand-in for "which resource/block", following this repo's own
// versions.tf/oci-security-list-*.tf/oci-containerengine-nodepool.tf/
// variables.tf/budget.tf naming convention) and Field (the tracker's
// human-chosen field name — a stand-in for "which attribute", conventionally
// echoing the underlying HCL attribute, e.g. "node-pool-boot-volume-size" for
// node_source_details.boot_volume_size_in_gbs). A production deployment
// wires its own tracker Field names to match; DefaultRiskRules ships the
// dogfood repo's conventions as the working example.
func DefaultRiskRules() []RiskRule {
	return append(append(replaceDestroyRiskRules(), securityRiskRules()...), costTripwireRiskRules()...)
}

// replaceDestroyRiskRules covers acceptance criterion 2: a change that
// removes a resource, or alters a replacement-forcing attribute, is
// replace/destroy.
func replaceDestroyRiskRules() []RiskRule {
	return []RiskRule{
		{
			Name:        "resource-removed",
			Risk:        RiskReplaceDestroy,
			Kinds:       []Kind{KindResource},
			ChangeTypes: []domain.ChangeType{domain.ChangeTypeRemoved},
		},
		{
			// Replacement-forcing attributes vary by provider/resource; this
			// is an example pattern (OCI's compute/node-pool resources force
			// replacement on availability_domain/shape changes), not an
			// exhaustive or hardcoded list — onboard more by adding a
			// pattern alternative here or a second rule, never new
			// classifier code.
			Name:         "replacement-forcing-attribute",
			Risk:         RiskReplaceDestroy,
			Kinds:        []Kind{KindResource},
			FieldPattern: `(?i)(availability[_-]?domain|node[_-]?shape\b)`,
		},
	}
}

// securityRiskRules covers acceptance criterion 3: a change widening a
// security-list source/destination to 0.0.0.0/0, or opening SSH, is
// security.
func securityRiskRules() []RiskRule {
	return []RiskRule{
		{
			Name:         "security-list-widened-to-world",
			Risk:         RiskSecurity,
			FieldPattern: `(?i)(source|destination|cidr)`,
			ValuePattern: `0\.0\.0\.0/0`,
		},
		{
			Name:         "ssh-port-opened",
			Risk:         RiskSecurity,
			FieldPattern: `(?i)ssh`,
			ValuePattern: `^22$`,
		},
	}
}

// costTripwireRiskRules covers acceptance criterion 4: a change to node
// count/size, OCPU, memory, boot-volume size, or budget amount is cost
// tripwire.
func costTripwireRiskRules() []RiskRule {
	return []RiskRule{
		{
			Name:         "cost-tripwire-attribute",
			Risk:         RiskCostTripwire,
			FieldPattern: `(?i)(\bcount\b|\bsize\b|\bocpus?\b|memory[_-]?in[_-]?gbs|boot[_-]?volume|\bbudget\b|\bamount\b)`,
		},
	}
}
