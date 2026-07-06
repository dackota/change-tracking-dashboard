// Package dashboardstats computes headline KPI metrics from a set of
// Changesets. This module is pure — no git, storage, or HTTP I/O, no side
// effects — and performs no mutation of its input.
package dashboardstats

import (
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
)

// Metrics carries the headline KPI values computed over a set of
// Changesets: the total Changeset and Change counts, the number of distinct
// repositories represented, the count of Chart-kind Changes, and the most
// recent commit timestamp. The zero Metrics is the correct result for an
// empty Changeset set.
type Metrics struct {
	// Changesets is the number of Changesets in the input.
	Changesets int
	// Changes is the total number of Changes across all Changesets.
	Changes int
	// Repositories is the number of distinct Changeset.Repo values.
	Repositories int
	// ChartChanges is the number of Changes whose Kind is changeset.KindChart.
	ChartChanges int
	// LastChangeAt is the maximum Changeset.CommittedAt across the input.
	// It is the zero time.Time when the input is empty.
	LastChangeAt time.Time
}

// Compute aggregates headline KPI Metrics from changesets. It is pure: it
// never mutates changesets or any of its elements, is deterministic for a
// given input regardless of order, and degrades to the zero Metrics
// (including a zero LastChangeAt) when changesets is empty.
func Compute(changesets []changeset.Changeset) Metrics {
	m := Metrics{Changesets: len(changesets)}

	repos := make(map[string]struct{}, len(changesets))
	for _, cs := range changesets {
		repos[cs.Repo] = struct{}{}

		m.Changes += len(cs.Changes)
		for _, c := range cs.Changes {
			if c.Kind == changeset.KindChart {
				m.ChartChanges++
			}
		}

		if cs.CommittedAt.After(m.LastChangeAt) {
			m.LastChangeAt = cs.CommittedAt
		}
	}
	m.Repositories = len(repos)

	return m
}
