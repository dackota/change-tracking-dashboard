// Package changeset assembles domain.Changes produced by a single commit
// into Changesets and classifies each Change's kind. This module is pure —
// no git, storage, or HTTP I/O, no side effects — and performs no mutation
// of its inputs.
package changeset

import (
	"sort"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/issueref"
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
	// IssueRefs is the issue/PR references parsed from this commit's message
	// (see internal/issueref), hoisted here from the constituent Changes'
	// IssueRefs the same way Author/CommittedAt are hoisted — every Change in
	// a Changeset comes from the same commit and so carries the same
	// IssueRefs. Nil/empty when the commit message had no reference.
	IssueRefs []string
	// Subject is the commit message's first line, hoisted here the same way
	// Author/CommittedAt/IssueRefs are hoisted — every Change in a Changeset
	// shares one commit. Empty when the commit predates #85 or otherwise has
	// no recorded subject; callers fall back to the SHA in that case.
	Subject string
	Changes []Change
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
				IssueRefs:   issueref.Copy(c.IssueRefs),
				Subject:     c.Subject,
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
	c.IssueRefs = issueref.Copy(c.IssueRefs)

	return Change{Change: c, Kind: ClassifyKind(c.FilePath)}
}
