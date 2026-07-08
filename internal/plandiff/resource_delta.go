// Package plandiff (this file): the resource-level delta classification
// (acceptance criteria 1 and 2) -- pure, in-memory pairing of two Resource
// sets by (Type, Name) identity into added/removed/changed buckets, with the
// replacement-forcing heuristic applied. This is a distinct concern from
// manifestdiff's own (Kind, Namespace, Name) pairing (render.go maps
// Resources onto manifestdiff.Manifest for the unified-diff text):
// manifestdiff computes a generic line-diff a caller renders, with no notion
// of "forces replacement"; this file computes plandiff's own
// added/removed/changed/replaced counts, which need the resource's
// top-level Attrs (Resource.Attrs), a concept manifestdiff never sees.
package plandiff

import "sort"

// resourceID is a Resource's (Type, Name) identity.
type resourceID struct {
	Type string
	Name string
}

// resourceDelta computes the per-resource classification and aggregate
// Summary between oldResources and newResources, pairing by (Type, Name)
// identity. forceAttrs is the set of attribute names whose change on an
// existing resource flags it ForcesReplacement (Config.ForceReplacementAttrs,
// resolved into a set for O(1) lookup). The returned Resources slice is
// sorted by (ResourceType, ResourceName) for deterministic output.
//
// Duplicate identity within one side's slice: the last Resource seen for
// that identity wins, mirroring manifestdiff's own documented convention for
// the same (upstream, HCL-level) invariant violation.
func resourceDelta(oldResources, newResources []Resource, forceAttrs map[string]struct{}) ([]ResourceDelta, Summary) {
	oldByID := indexByID(oldResources)
	newByID := indexByID(newResources)

	ids := make(map[resourceID]struct{}, len(oldByID)+len(newByID))
	for id := range oldByID {
		ids[id] = struct{}{}
	}
	for id := range newByID {
		ids[id] = struct{}{}
	}

	sortedIDs := make([]resourceID, 0, len(ids))
	for id := range ids {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Slice(sortedIDs, func(i, j int) bool {
		a, b := sortedIDs[i], sortedIDs[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Name < b.Name
	})

	var deltas []ResourceDelta
	var summary Summary
	for _, id := range sortedIDs {
		oldR, inOld := oldByID[id]
		newR, inNew := newByID[id]

		switch {
		case inOld && inNew:
			if oldR.Body == newR.Body {
				continue // identical resource: no delta at all
			}
			forces := attrChanged(oldR.Attrs, newR.Attrs, forceAttrs)
			deltas = append(deltas, ResourceDelta{ResourceType: id.Type, ResourceName: id.Name, Kind: ResourceChanged, ForcesReplacement: forces})
			summary.Changed++
			if forces {
				summary.Replaced++
			}
		case inOld: // removed: always flags replacement (a destroy is inherently destructive; PRD R13/R18)
			deltas = append(deltas, ResourceDelta{ResourceType: id.Type, ResourceName: id.Name, Kind: ResourceRemoved, ForcesReplacement: true})
			summary.Removed++
			summary.Replaced++
		case inNew: // added: never flags replacement
			deltas = append(deltas, ResourceDelta{ResourceType: id.Type, ResourceName: id.Name, Kind: ResourceAdded})
			summary.Added++
		}
	}

	return deltas, summary
}

// indexByID maps each Resource's identity to itself for O(1) cross-set
// lookup, keeping the last Resource seen for a duplicate identity.
func indexByID(resources []Resource) map[resourceID]Resource {
	out := make(map[resourceID]Resource, len(resources))
	for _, r := range resources {
		out[resourceID{Type: r.Type, Name: r.Name}] = r
	}
	return out
}

// attrChanged reports whether any attribute name in forceAttrs has a
// different rendered value between oldAttrs and newAttrs. An attribute
// absent from one side (added or removed, not just modified) counts as
// changed too -- e.g. removing an explicit availability_domain override
// entirely is exactly the kind of change this heuristic exists to catch.
func attrChanged(oldAttrs, newAttrs map[string]string, forceAttrs map[string]struct{}) bool {
	for name := range forceAttrs {
		if oldAttrs[name] != newAttrs[name] {
			return true
		}
	}
	return false
}

// forceAttrSet converts Config.ForceReplacementAttrs (a resolved, non-nil
// slice per Config.Resolved) into a set for O(1) membership checks.
func forceAttrSet(attrs []string) map[string]struct{} {
	out := make(map[string]struct{}, len(attrs))
	for _, a := range attrs {
		out[a] = struct{}{}
	}
	return out
}
