// Package changeset (this file): Risk classification. Like Kind, Risk is a
// pure, query-time projection over an already-assembled Changeset — never
// stored — derived by evaluating a config-driven table of RiskRules against
// each Change. The classifier itself carries no resource-type/attribute
// knowledge of its own: every pattern that decides "this is a cost tripwire"
// or "this widens a security-list to 0.0.0.0/0" lives in the RiskRule data
// (see risk_rules.go's DefaultRiskRules), so onboarding a new resource,
// attribute, or provider is a data change, never a code change.
package changeset

import (
	"regexp"
	"sort"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// Risk is a risk classification derived for a Changeset.
type Risk string

const (
	// RiskReplaceDestroy marks a change that removes a resource or alters an
	// attribute known to force replacement (downtime/data-loss risk).
	RiskReplaceDestroy Risk = "replace/destroy"
	// RiskSecurity marks a change that widens network/IAM exposure (e.g. a
	// security-list source/destination widened to 0.0.0.0/0, or SSH opened).
	RiskSecurity Risk = "security"
	// RiskCostTripwire marks a change to a cost-relevant attribute (node
	// count/size, OCPU, memory, boot-volume size, budget amount, …).
	RiskCostTripwire Risk = "cost tripwire"
)

// RiskRule is one config-driven rule: a Change matching every non-empty
// predicate field fires Risk. Every predicate field is optional ("" or nil
// means "matches anything" along that dimension), so a rule can combine any
// subset of Kind/ChangeType/FilePath-pattern/Field-pattern/Value-pattern
// restrictions. Patterns are Go regexp syntax, matched with MatchString
// (substring search, not full-string anchoring) unless the pattern itself
// anchors with ^/$.
//
// RiskRule is the "configuration" acceptance criterion 5 requires: it is
// plain data (loadable from YAML/JSON as-is, or — as shipped today — an
// embedded Go struct literal in risk_rules.go); the classifier that
// evaluates it (ClassifyRisk) contains no resource-specific logic at all.
type RiskRule struct {
	// Name documents the rule's intent (e.g. "cost-node-pool-size"). Not
	// matched against anything; purely for readability/debugging.
	Name string

	// Risk is the classification this rule yields when every predicate below
	// matches.
	Risk Risk

	// Kinds restricts the rule to one or more Kinds (e.g. KindResource).
	// Empty matches any Kind.
	Kinds []Kind
	// ChangeTypes restricts the rule to one or more domain.ChangeTypes (e.g.
	// ChangeTypeRemoved). Empty matches any ChangeType.
	ChangeTypes []domain.ChangeType

	// FilePathPattern, matched against the Change's FilePath, is the
	// "resource-type" axis (e.g. a security-list resource file). Empty
	// matches any FilePath.
	FilePathPattern string
	// FieldPattern, matched against the Change's Field, is the "attribute"
	// axis (e.g. an attribute named like "boot_volume_size_in_gbs"). Empty
	// matches any Field.
	FieldPattern string
	// ValuePattern is matched against the Change's NewValue, falling back to
	// OldValue when NewValue is nil (e.g. a removal) — so a rule can key off
	// the value content itself (e.g. "0.0.0.0/0"). Empty matches any value,
	// including a Change with neither value set.
	ValuePattern string
}

// ClassifyRisk evaluates rules against every Change in cs and returns the
// resulting set of Risk classes: deduplicated and sorted for a stable,
// deterministic result regardless of Change or rule order. ClassifyRisk is a
// total, pure function — an empty/nil Changes slice or an empty/nil rules
// slice yields a nil (empty) result, never a panic or error, and an
// unparseable regex pattern in a rule is treated as "never matches" for that
// rule rather than failing the whole classification (production
// configuration is validated at load time via ValidateRiskRules; this
// defensive fallback only protects ClassifyRisk's own total-function
// contract against a malformed rule reaching it some other way, e.g. a test
// double).
func ClassifyRisk(cs Changeset, rules []RiskRule) []Risk {
	found := make(map[Risk]struct{})
	for _, rule := range rules {
		for _, c := range cs.Changes {
			if ruleMatches(rule, c) {
				found[rule.Risk] = struct{}{}
				break // one match per rule is enough; move to the next rule
			}
		}
	}
	return sortedRisks(found)
}

// ruleMatches reports whether Change c satisfies every predicate on rule.
func ruleMatches(rule RiskRule, c Change) bool {
	if len(rule.Kinds) > 0 && !containsKind(rule.Kinds, c.Kind) {
		return false
	}
	if len(rule.ChangeTypes) > 0 && !containsChangeType(rule.ChangeTypes, c.ChangeType) {
		return false
	}
	if !matchesPattern(rule.FilePathPattern, c.FilePath) {
		return false
	}
	if !matchesPattern(rule.FieldPattern, c.Field) {
		return false
	}
	if !matchesPattern(rule.ValuePattern, changeValue(c)) {
		return false
	}
	return true
}

// changeValue returns the Change's NewValue, falling back to OldValue when
// NewValue is nil (e.g. a "removed" Change), or "" when neither is set.
func changeValue(c Change) string {
	if c.NewValue != nil {
		return *c.NewValue
	}
	if c.OldValue != nil {
		return *c.OldValue
	}
	return ""
}

// matchesPattern reports whether pattern matches s. An empty pattern always
// matches (the "any" predicate). An unparseable pattern never matches —
// ClassifyRisk stays total even if a malformed rule reaches it.
func matchesPattern(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

// containsKind reports whether kinds contains k.
func containsKind(kinds []Kind, k Kind) bool {
	for _, want := range kinds {
		if want == k {
			return true
		}
	}
	return false
}

// containsChangeType reports whether types contains t.
func containsChangeType(types []domain.ChangeType, t domain.ChangeType) bool {
	for _, want := range types {
		if want == t {
			return true
		}
	}
	return false
}

// sortedRisks converts the found-set into a deterministically ordered slice.
// A nil/empty set yields a nil slice, not an empty-but-non-nil one, matching
// the "zero risk classes" contract (acceptance criterion 6) as the natural
// Go zero value.
func sortedRisks(found map[Risk]struct{}) []Risk {
	if len(found) == 0 {
		return nil
	}
	out := make([]Risk, 0, len(found))
	for r := range found {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ValidateRiskRules compiles every non-empty pattern in rules and returns an
// error naming the first invalid one. Callers that load RiskRules from an
// external source (e.g. a YAML config file) should call this once at load
// time to fail fast on a typo'd regex, rather than have ClassifyRisk quietly
// treat it as "never matches" forever.
func ValidateRiskRules(rules []RiskRule) error {
	for _, rule := range rules {
		for _, pattern := range []string{rule.FilePathPattern, rule.FieldPattern, rule.ValuePattern} {
			if pattern == "" {
				continue
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return &InvalidRiskRuleError{RuleName: rule.Name, Pattern: pattern, Err: err}
			}
		}
	}
	return nil
}

// InvalidRiskRuleError names the rule and pattern that failed to compile, so
// a config-load failure is actionable without leaking regexp internals
// beyond the compile error itself.
type InvalidRiskRuleError struct {
	RuleName string
	Pattern  string
	Err      error
}

func (e *InvalidRiskRuleError) Error() string {
	return "changeset: risk rule " + e.RuleName + ": invalid pattern " + e.Pattern + ": " + e.Err.Error()
}

func (e *InvalidRiskRuleError) Unwrap() error { return e.Err }
