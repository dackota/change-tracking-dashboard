// Package changeset assembles domain.Changes produced by a single commit
// into Changesets and classifies each Change's kind. This module is pure —
// no git, storage, or HTTP I/O, no side effects — and performs no mutation
// of its inputs.
package changeset

import (
	"sort"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
)

// Change is a domain.Change projected with its classified Kind. It embeds
// domain.Change so callers get the full Change fields plus Kind.
type Change struct {
	domain.Change
	Kind Kind
}

// Changeset is all the Changes produced by a single commit — the unit a
// timeline flag represents. It is a query-time projection over a slice of
// domain.Change: pure, computed from its input, and never mutates it.
type Changeset struct {
	Repo        string
	CommitSha   string
	Author      string
	CommittedAt time.Time
	Changes     []Change
}

// commitKey identifies the commit that produced a Change. A CommitSha alone
// is not guaranteed globally unique across repos, so grouping keys on the
// pair.
type commitKey struct {
	repo      string
	commitSha string
}

// Assemble groups changes by the commit that produced them (Repo,
// CommitSha) into Changesets. Each Changeset carries the commit metadata
// (Repo, CommitSha, Author, CommittedAt) plus its slice of Changes,
// classified by Kind. Assemble is pure: it never mutates the input slice or
// any of its Changes, and always returns newly allocated values.
func Assemble(changes []domain.Change) []Changeset {
	order := make([]commitKey, 0)
	grouped := make(map[commitKey]*Changeset)

	for _, c := range changes {
		key := commitKey{repo: c.Repo, commitSha: c.CommitSha}

		cs, ok := grouped[key]
		if !ok {
			cs = &Changeset{
				Repo:        c.Repo,
				CommitSha:   c.CommitSha,
				Author:      c.Author,
				CommittedAt: c.CommittedAt,
			}
			grouped[key] = cs
			order = append(order, key)
		}

		cs.Changes = append(cs.Changes, newChange(c))
	}

	out := make([]Changeset, 0, len(order))
	for _, key := range order {
		out = append(out, *grouped[key])
	}

	// Deterministic order: most-recent-first by CommittedAt, ties broken
	// stably by CommitSha ascending so output is reproducible regardless of
	// input order.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CommittedAt.Equal(out[j].CommittedAt) {
			return out[i].CommittedAt.After(out[j].CommittedAt)
		}
		return out[i].CommitSha < out[j].CommitSha
	})

	return out
}

// newChange builds a projected Change from a domain.Change, classifying its
// Kind. It defensively copies the Facets map so the returned Change never
// aliases the caller's input — callers may freely mutate the result without
// affecting the original domain.Change.
func newChange(c domain.Change) Change {
	facets := make(map[string]string, len(c.Facets))
	for k, v := range c.Facets {
		facets[k] = v
	}
	c.Facets = facets

	return Change{Change: c, Kind: ClassifyKind(c.FilePath)}
}
