// Package differ compares old and new TrackedField values to produce Change
// records. This module is pure — no I/O, no side effects.
//
// Two diff functions are provided:
//   - DiffScalar: scalar TrackedField → 0 or 1 Change.
//   - DiffKeyed:  keyed TrackedField (Map != nil) → 0..N Changes, one per affected key.
//
// Both share ScalarParams for commit-level metadata. The Poller dispatches to
// the correct function based on TrackedField.IsKeyed().
package differ

import (
	"sort"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/issueref"
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
	IssueRefs   []string          // issue/PR references parsed from the triggering commit's message (see internal/issueref); nil when none
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

// DiffKeyed compares old and new keyed TrackedFields (where Map != nil) and
// returns one Change per affected map key:
//   - key in both, value changed  → modified (Key set, old+new values)
//   - key only in new             → added    (Key set, nil old)
//   - key only in old             → removed  (Key set, nil new)
//   - key in both, value same     → no Change
//
// The output slice is sorted by key for deterministic ordering. Scalar Changes
// continue to use Key=nil; only keyed Changes have Key set.
//
// DiffKeyed never mutates its inputs.
func DiffKeyed(p ScalarParams, old, new domain.TrackedField) []domain.Change {
	oldMap := old.Map
	newMap := new.Map

	// Collect all keys across both maps.
	keySet := make(map[string]struct{}, len(oldMap)+len(newMap))
	for k := range oldMap {
		keySet[k] = struct{}{}
	}
	for k := range newMap {
		keySet[k] = struct{}{}
	}

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var changes []domain.Change
	for _, k := range keys {
		oldVal, inOld := oldMap[k]
		newVal, inNew := newMap[k]

		kCopy := k // avoid loop-variable capture

		switch {
		case inOld && inNew && oldVal == newVal:
			// Unchanged — emit nothing.

		case inOld && inNew:
			// Modified.
			ov, nv := oldVal, newVal
			changes = append(changes, newKeyedChange(p, &kCopy, domain.ChangeTypeModified, &ov, &nv))

		case !inOld && inNew:
			// Added.
			nv := newVal
			changes = append(changes, newKeyedChange(p, &kCopy, domain.ChangeTypeAdded, nil, &nv))

		case inOld && !inNew:
			// Removed.
			ov := oldVal
			changes = append(changes, newKeyedChange(p, &kCopy, domain.ChangeTypeRemoved, &ov, nil))
		}
	}

	return changes
}

// newKeyedChange constructs an immutable Change with a non-nil Key.
func newKeyedChange(p ScalarParams, key *string, ct domain.ChangeType, oldVal, newVal *string) domain.Change {
	c := newChange(p, ct, oldVal, newVal)
	c.Key = key
	return c
}

// newChange constructs an immutable Change from the provided params and values.
// Key is always nil here; callers that need a non-nil key use newKeyedChange.
func newChange(p ScalarParams, ct domain.ChangeType, oldVal, newVal *string) domain.Change {
	// Copy facets defensively — do not reference the caller's map.
	facets := make(map[string]string, len(p.Facets))
	for k, v := range p.Facets {
		facets[k] = v
	}

	// Copy issue refs defensively — do not reference the caller's slice. See
	// issueref.Copy for the nil-preserving contract.
	issueRefs := issueref.Copy(p.IssueRefs)

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
		IssueRefs:   issueRefs,
	}
}
