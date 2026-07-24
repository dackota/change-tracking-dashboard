// Package web (this file): the risk-rule source seam. Risk is a query-time
// projection (changeset.ClassifyRisk) the feed and detail handlers compute on
// every read. Rather than bind those handlers to the concrete config watcher —
// or to the hardcoded changeset.DefaultRiskRules — they depend on this small
// RiskRulesSource interface. Production wires *config.Watcher (so handlers see
// the live, hot-reloaded rule set); tests inject a static fake; and a nil
// source degrades to the built-in defaults, keeping zero-config callers working.
package web

import "github.com/dackota/change-tracking-dashboard/internal/changeset"

// RiskRulesSource supplies the risk-rule set used to classify a Changeset at
// read time. *config.Watcher satisfies it via its RiskRules() method, so the
// handlers always classify against the current configuration without capturing
// a stale snapshot at construction.
type RiskRulesSource interface {
	RiskRules() []changeset.RiskRule
}

// riskRulesOrDefault returns src's current rules, or the built-in
// changeset.DefaultRiskRules when src is nil. The nil path is the zero-config
// default that keeps existing callers and tests (which construct handlers
// without a source) classifying exactly as before.
func riskRulesOrDefault(src RiskRulesSource) []changeset.RiskRule {
	if src == nil {
		return changeset.DefaultRiskRules()
	}
	return src.RiskRules()
}
