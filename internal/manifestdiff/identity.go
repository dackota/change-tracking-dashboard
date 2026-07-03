package manifestdiff

import "sort"

// identity is a manifest's (Kind, Namespace, Name) triple — the key manifests
// are paired by, regardless of input order. pairManifests assumes identity is
// unique within a single side's manifest set, matching chartrender's
// guarantee that a normalized manifest set has no duplicate
// (Kind, Namespace, Name); a duplicate identity on one side silently keeps
// the last manifest seen for that identity, an accepted, documented
// consequence of that upstream invariant.
type identity struct {
	Kind      string
	Namespace string
	Name      string
}

// pair is one identity's YAML on each side, with presence flags
// distinguishing "absent from this side" from "present with empty YAML"
// (which should not occur in well-formed input, but pair does not assume it
// can't).
type pair struct {
	id      identity
	oldYAML string
	inOld   bool
	newYAML string
	inNew   bool
}

// pairManifests pairs old and new manifests by identity and returns one pair
// per identity present in either side, sorted by (Kind, Namespace, Name).
// Pairing by identity rather than position — and never concatenating the two
// sides into a single blob — is what lets a reordered-but-equal manifest set
// produce no diff, and what keeps an add/remove from ever perturbing an
// unrelated manifest's line counts: each identity's comparison is fully
// self-contained, with no separator token between identities to leak into
// the diffed content or its counts.
func pairManifests(oldManifests, newManifests []Manifest) []pair {
	oldByID := indexByIdentity(oldManifests)
	newByID := indexByIdentity(newManifests)

	ids := make(map[identity]struct{}, len(oldByID)+len(newByID))
	for id := range oldByID {
		ids[id] = struct{}{}
	}
	for id := range newByID {
		ids[id] = struct{}{}
	}

	sortedIDs := make([]identity, 0, len(ids))
	for id := range ids {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Slice(sortedIDs, func(i, j int) bool {
		a, b := sortedIDs[i], sortedIDs[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	pairs := make([]pair, 0, len(sortedIDs))
	for _, id := range sortedIDs {
		oldYAML, inOld := oldByID[id]
		newYAML, inNew := newByID[id]
		pairs = append(pairs, pair{id: id, oldYAML: oldYAML, inOld: inOld, newYAML: newYAML, inNew: inNew})
	}
	return pairs
}

// indexByIdentity maps each manifest's identity to its YAML for O(1)
// cross-set lookup.
func indexByIdentity(manifests []Manifest) map[identity]string {
	out := make(map[identity]string, len(manifests))
	for _, m := range manifests {
		out[identity{Kind: m.Kind, Namespace: m.Namespace, Name: m.Name}] = m.YAML
	}
	return out
}

// countChangedManifests returns the manifests-changed count: manifests
// present on both sides whose YAML differs, plus manifests added, plus
// manifests removed. It is derived from the same pairs renderPairs consumes
// so the two can never disagree about which identities changed.
func countChangedManifests(pairs []pair) int {
	changed := 0
	for _, p := range pairs {
		if p.inOld && p.inNew {
			if p.oldYAML != p.newYAML {
				changed++
			}
			continue
		}
		changed++ // present on exactly one side: added or removed
	}
	return changed
}
