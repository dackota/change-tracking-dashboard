package manifestdiff

import (
	"sort"
	"strings"
)

// identity is a manifest's (Kind, Namespace, Name) triple — the key manifests
// are paired by, regardless of input order. Diff assumes identity is unique
// within a single side's manifest set, matching chartrender's guarantee that
// a normalized manifest set has no duplicate (Kind, Namespace, Name).
type identity struct {
	Kind      string
	Namespace string
	Name      string
}

// sortedCopy returns a new slice containing manifests sorted by
// (Kind, Namespace, Name, YAML) — the same order chartrender's normalization
// uses. It never mutates manifests: Diff must not mutate the caller's
// slices, and defensive sorting proves manifest identity, not input order,
// drives the comparison.
func sortedCopy(manifests []Manifest) []Manifest {
	out := make([]Manifest, len(manifests))
	copy(out, manifests)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.YAML < b.YAML
	})
	return out
}

// countChangedManifests returns the number of manifests whose YAML differs
// between old and new, plus manifests present only in new (added) plus
// manifests present only in old (removed) — the manifests-changed count.
func countChangedManifests(old, new []Manifest) int {
	oldByID := indexByIdentity(old)
	newByID := indexByIdentity(new)

	changed := 0
	for id, oldYAML := range oldByID {
		newYAML, ok := newByID[id]
		switch {
		case !ok:
			changed++ // removed
		case oldYAML != newYAML:
			changed++ // modified
		}
	}
	for id := range newByID {
		if _, ok := oldByID[id]; !ok {
			changed++ // added
		}
	}
	return changed
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

// concatManifests joins an identity-sorted manifest set into a single
// "---\n"-separated text — the unit lineDiff compares. Because both sides are
// sorted by the same identity order, two manifest sets with identical
// content produce byte-identical text regardless of the order manifests
// arrived in.
func concatManifests(manifests []Manifest) string {
	var b strings.Builder
	for i, m := range manifests {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(m.YAML)
		if len(m.YAML) > 0 && !strings.HasSuffix(m.YAML, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}
