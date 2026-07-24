package config_test

import (
	"strings"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/config"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

// riskRulesYAML is a minimal valid config carrying one custom risk rule that
// exercises every predicate dimension the YAML shape supports.
const riskRulesYAML = `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
riskRules:
  - name: chart-major-bump
    risk: major version bump
    kinds: [chart]
    changeTypes: [modified]
    fieldPattern: chartDependencies
    semverBump: major
trackers:
  - repo: /some/repo
    files:
      - glob: 'apps/Chart.yaml'
        fields:
          - name: chartDependencies
            expr: '.version'
`

// findRule returns the first rule with the given name, or nil.
func findRule(rules []changeset.RiskRule, name string) *changeset.RiskRule {
	for i := range rules {
		if rules[i].Name == name {
			return &rules[i]
		}
	}
	return nil
}

// TestLoad_RiskRules_ParsedAndAppendedToDefaults proves a configured riskRules
// entry is parsed into Config.RiskRules and augments (rather than replaces) the
// built-in DefaultRiskRules — so an operator adds a rule without losing the
// shipped OCI/semver classifications.
func TestLoad_RiskRules_ParsedAndAppendedToDefaults(t *testing.T) {
	path := writeTemp(t, riskRulesYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	got := w.Current().RiskRules
	if want := len(changeset.DefaultRiskRules()) + 1; len(got) != want {
		t.Fatalf("len(RiskRules) = %d, want %d (defaults + 1 configured)", len(got), want)
	}

	rule := findRule(got, "chart-major-bump")
	if rule == nil {
		t.Fatalf("configured rule %q not found in RiskRules %v", "chart-major-bump", got)
	}
	if rule.Risk != changeset.RiskMajorVersionBump {
		t.Errorf("rule.Risk = %q, want %q", rule.Risk, changeset.RiskMajorVersionBump)
	}
	if rule.SemverBump != changeset.SemverBumpMajor {
		t.Errorf("rule.SemverBump = %q, want %q", rule.SemverBump, changeset.SemverBumpMajor)
	}
	if len(rule.Kinds) != 1 || rule.Kinds[0] != changeset.KindChart {
		t.Errorf("rule.Kinds = %v, want [%q]", rule.Kinds, changeset.KindChart)
	}
	if len(rule.ChangeTypes) != 1 || rule.ChangeTypes[0] != domain.ChangeTypeModified {
		t.Errorf("rule.ChangeTypes = %v, want [%q]", rule.ChangeTypes, domain.ChangeTypeModified)
	}
	if rule.FieldPattern != "chartDependencies" {
		t.Errorf("rule.FieldPattern = %q, want %q", rule.FieldPattern, "chartDependencies")
	}
}

// TestLoad_NoRiskRules_UsesDefaults proves omitting the riskRules section (every
// existing ConfigMap) yields exactly the built-in DefaultRiskRules — including
// the semver-major-bump rule — so behavior is unchanged when unset.
func TestLoad_NoRiskRules_UsesDefaults(t *testing.T) {
	path := writeTemp(t, minimalValidYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	got := w.Current().RiskRules
	if len(got) != len(changeset.DefaultRiskRules()) {
		t.Fatalf("len(RiskRules) = %d, want %d (defaults only)", len(got), len(changeset.DefaultRiskRules()))
	}
	if findRule(got, "semver-major-bump") == nil {
		t.Errorf("default semver-major-bump rule missing from RiskRules when riskRules unset")
	}
}

// TestCurrent_RiskRules_IndependentCopy proves Current() hands out a deep copy
// of the risk rules — mutating a returned rule's slice fields cannot corrupt
// the live snapshot the web handlers read on the next request.
func TestCurrent_RiskRules_IndependentCopy(t *testing.T) {
	path := writeTemp(t, riskRulesYAML)

	w, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	first := findRule(w.Current().RiskRules, "chart-major-bump")
	if first == nil || len(first.Kinds) == 0 {
		t.Fatalf("expected configured rule with Kinds, got %+v", first)
	}
	first.Kinds[0] = changeset.KindResource // mutate the returned copy

	second := findRule(w.Current().RiskRules, "chart-major-bump")
	if second.Kinds[0] != changeset.KindChart {
		t.Errorf("Kinds[0] = %q, want %q (mutation of a prior copy leaked into the live config)", second.Kinds[0], changeset.KindChart)
	}
}

// TestLoad_InvalidRiskRule_ReturnsError proves each malformed predicate is
// rejected at load with an actionable error rather than silently never firing.
func TestLoad_InvalidRiskRule_ReturnsError(t *testing.T) {
	base := `
defaults:
  pollIntervalSeconds: 60
  backfillDays: 90
trackers:
  - repo: /some/repo
    files:
      - glob: 'apps/Chart.yaml'
        fields:
          - name: chartDependencies
            expr: '.version'
`
	cases := map[string]string{
		"unknown kind":       "riskRules:\n  - name: r\n    risk: x\n    kinds: [nonsense]\n",
		"unknown changeType": "riskRules:\n  - name: r\n    risk: x\n    changeTypes: [exploded]\n",
		"unknown semverBump": "riskRules:\n  - name: r\n    risk: x\n    semverBump: majro\n",
		"bad regex":          "riskRules:\n  - name: r\n    risk: x\n    fieldPattern: '('\n",
		"missing risk":       "riskRules:\n  - name: r\n    fieldPattern: foo\n",
	}

	for name, block := range cases {
		block := block
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, base+block)
			if _, err := config.Load(path); err == nil {
				t.Fatalf("Load(%s) = nil error, want a validation error", name)
			} else if !strings.Contains(err.Error(), "config:") {
				t.Errorf("error %q is not a config-load error", err)
			}
		})
	}
}
