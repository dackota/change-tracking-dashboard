// risk_rules.go parses the optional top-level `riskRules:` block into
// changeset.RiskRule values and appends them to the built-in DefaultRiskRules.
// Config rules AUGMENT the defaults rather than replacing them, so an operator
// can add a classification (e.g. a semver major-version bump on their own
// fields) without re-declaring — or losing — the shipped rule set. Every rule
// is validated at load time via changeset.ValidateRiskRules so a typo'd
// predicate fails fast with an actionable error instead of silently never
// firing.
package config

import (
	"fmt"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// RiskRuleRaw is the raw YAML shape for one entry in the `riskRules:` list. It
// mirrors changeset.RiskRule but takes plain strings for the enum-typed fields
// (risk/kinds/changeTypes/semverBump) so the YAML stays free of Go type names;
// resolveRiskRules converts and validates them. Every field is optional except
// that a rule with no effective predicate would match everything — validation
// requires a non-empty risk but leaves predicate breadth to the operator.
type RiskRuleRaw struct {
	Name            string   `yaml:"name"`
	Risk            string   `yaml:"risk"`
	Kinds           []string `yaml:"kinds"`
	ChangeTypes     []string `yaml:"changeTypes"`
	FilePathPattern string   `yaml:"filePathPattern"`
	FieldPattern    string   `yaml:"fieldPattern"`
	ValuePattern    string   `yaml:"valuePattern"`
	SemverBump      string   `yaml:"semverBump"`
}

// resolveRiskRules converts raw into changeset.RiskRule values, validates each,
// and returns the built-in DefaultRiskRules followed by the configured rules.
// An absent/empty raw list yields exactly the defaults. The first invalid rule
// aborts the load with an error identifying the offending entry.
func resolveRiskRules(raw []RiskRuleRaw) ([]changeset.RiskRule, error) {
	rules := changeset.DefaultRiskRules()
	for i, r := range raw {
		rule := changeset.RiskRule{
			Name:            r.Name,
			Risk:            changeset.Risk(r.Risk),
			FilePathPattern: r.FilePathPattern,
			FieldPattern:    r.FieldPattern,
			ValuePattern:    r.ValuePattern,
			SemverBump:      changeset.SemverBumpLevel(r.SemverBump),
		}
		for _, k := range r.Kinds {
			rule.Kinds = append(rule.Kinds, changeset.Kind(k))
		}
		for _, ct := range r.ChangeTypes {
			rule.ChangeTypes = append(rule.ChangeTypes, domain.ChangeType(ct))
		}
		if err := changeset.ValidateRiskRules([]changeset.RiskRule{rule}); err != nil {
			return nil, fmt.Errorf("config: riskRules[%d] (name=%q): %w", i, r.Name, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
