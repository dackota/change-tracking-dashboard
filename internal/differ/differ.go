// Package differ compares old and new TrackedField values to produce Change
// records. This module is pure — no I/O, no side effects. It handles the
// scalar case; keyed-map diffing is a future slice (keyed-map-diff task).
//
// Interface design: ScalarParams carries commit metadata separately from the
// field values so the Differ's signature can be extended with a keyed variant
// (DiffKeyed) without reshaping the existing call sites.
package differ

import (
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
)

// ScalarParams carries the immutable commit-level metadata passed through to
// every Change produced by DiffScalar. Using a params struct lets future
// keyed-map variants share the same metadata shape without repeating arguments.
type ScalarParams struct {
	Repo        string
	FilePath    string
	Field       string
	CommitSha   string
	Author      string
	CommittedAt time.Time
	Facets      map[string]string // caller attaches facets after extraction
}

// DiffScalar compares an old and new scalar TrackedField for the same tracked
// field and returns a slice of 0 or 1 Changes. The returned slice is always a
// new allocation — never a reference into the input params.
//
// Rules:
//   - both absent        → []
//   - unchanged value    → []
//   - absent → present   → [{added}]
//   - present → absent   → [{removed}]
//   - value changed      → [{modified}]
func DiffScalar(p ScalarParams, old, new domain.TrackedField) []domain.Change {
	switch {
	case !old.Present && !new.Present:
		return nil

	case !old.Present && new.Present:
		v := new.Value
		return []domain.Change{newChange(p, domain.ChangeTypeAdded, nil, &v)}

	case old.Present && !new.Present:
		v := old.Value
		return []domain.Change{newChange(p, domain.ChangeTypeRemoved, &v, nil)}

	default: // both present
		if old.Value == new.Value {
			return nil
		}
		ov, nv := old.Value, new.Value
		return []domain.Change{newChange(p, domain.ChangeTypeModified, &ov, &nv)}
	}
}

// newChange constructs an immutable Change from the provided params and values.
// key is always nil for scalars; future keyed-map variants will pass a non-nil key.
func newChange(p ScalarParams, ct domain.ChangeType, oldVal, newVal *string) domain.Change {
	// Copy facets defensively — do not reference the caller's map.
	facets := make(map[string]string, len(p.Facets))
	for k, v := range p.Facets {
		facets[k] = v
	}

	return domain.Change{
		Repo:        p.Repo,
		FilePath:    p.FilePath,
		Field:       p.Field,
		Key:         nil, // always nil for scalars
		ChangeType:  ct,
		OldValue:    oldVal,
		NewValue:    newVal,
		Facets:      facets,
		CommitSha:   p.CommitSha,
		Author:      p.Author,
		CommittedAt: p.CommittedAt,
	}
}
