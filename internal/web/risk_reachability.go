// Package web (this file): the operator-facing warning for a risk filter that
// names a class no active rule can produce.
package web

import (
	"log/slog"
	"sort"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
)

// riskConfigRemedy tells an operator how to make a config-only risk class
// reachable. It travels with the warning so acting on it needs neither the
// README nor git history.
const riskConfigRemedy = `add a riskRules entry for this class (e.g. semverBump: major for major-version-bump) — see the README's "re-add it explicitly" recipe`

// warnUnreachableRiskFilter logs a warning for each requested risk slug that
// no rule in the active set can ever produce.
//
// This closes a quiet failure mode. A slug in the closed vocabulary is always
// accepted, so `?risk=major-version-bump` on a zero-config deployment returns
// 200 with an empty list — indistinguishable from "no breaking upgrades in
// this window", when it actually means "no rule here produces that class".
// The first is an answer; the second is a configuration problem the operator
// can fix, and until now nothing told them so.
//
// The signal goes to the logs rather than the response body on purpose. The
// request is valid and the empty list is a truthful answer to what was asked,
// so neither a 400 nor a response-shape change is warranted — and the party
// who can act on it is the operator, not the caller.
//
// It is deliberately quiet for a class the active rules DO produce: an empty
// result there really does mean "nothing matched", and a warning that fires on
// the common path is one operators learn to ignore. Reachability is judged
// against the ACTIVE rules, not the shipped defaults, so an operator who has
// configured the class is never warned about it.
func warnUnreachableRiskFilter(logger *slog.Logger, risks filter.ClassSet, rules []changeset.RiskRule) {
	if risks.Empty() {
		return
	}

	producible := changeset.ProducibleRisks(rules)

	// Collect first, then emit in sorted order, so a multi-class filter logs
	// deterministically rather than in map-iteration order.
	var unreachable []string
	for slug := range changeset.RiskSlugs() {
		if !risks.Allows(slug) {
			continue
		}
		risk, ok := changeset.RiskFromSlug(slug)
		if !ok {
			continue
		}
		if _, canProduce := producible[risk]; !canProduce {
			unreachable = append(unreachable, slug)
		}
	}
	sort.Strings(unreachable)

	for _, slug := range unreachable {
		logger.Warn(
			"web: risk filter names a class no configured rule can produce; the empty result means \"not configured\", not \"nothing matched\"",
			"risk", slug,
			"remedy", riskConfigRemedy,
		)
	}
}
