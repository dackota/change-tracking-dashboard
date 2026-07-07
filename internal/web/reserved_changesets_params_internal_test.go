// Package web (this file): a drift guard between this package's
// reservedChangesetsParams (api_changesets.go) and store.reservedFacetNames
// (internal/store/store_filter.go). internal/store cannot import internal/web
// (web already imports store — importing back would create a cycle), so the
// two "reserved query-param name" sets are hand-duplicated rather than
// sharing one Go value. This test derives its list of names directly from
// the reservedChangesetsParams package variable — never a re-typed string
// literal — so that adding a new reserved param to this set and forgetting
// the matching exclusion in store.reservedFacetNames fails here instead of
// silently reopening the facet-shadowing hole fixed by
// repo-param-facet-shadowing-fix (an admin-configured facet named e.g.
// "repo" shadowing the repo-dropdown's own query param).
package web

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// TestReservedChangesetsParams_ExcludedFromFacetOptions is the drift guard:
// for every name in reservedChangesetsParams, seed a Change whose Facets map
// uses that name as the facet key, then assert store.FacetOptions() never
// surfaces it. If store.reservedFacetNames is ever missing an entry that
// reservedChangesetsParams has (a one-sided addition), the corresponding
// subtest below fails.
func TestReservedChangesetsParams_ExcludedFromFacetOptions(t *testing.T) {
	t.Parallel()

	if len(reservedChangesetsParams) == 0 {
		t.Fatal("reservedChangesetsParams is empty — nothing to guard; the set may have moved or been renamed")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "reserved_guard_test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	oldVal, newVal := "a", "b"
	base := time.Now().Add(-time.Hour)

	i := 0
	for name := range reservedChangesetsParams {
		c := domain.Change{
			Repo:       "apps-repo",
			FilePath:   fmt.Sprintf("file-%d.yaml", i),
			Field:      "f",
			ChangeType: domain.ChangeTypeModified,
			OldValue:   &oldVal,
			NewValue:   &newVal,
			// Facets carries the reserved name plus a co-located non-reserved
			// facet ("region"), so the assertion below also proves the
			// exclusion stays narrowly scoped to reserved names.
			Facets:      map[string]string{name: "hijack-value", "region": "us-west-2"},
			CommitSha:   fmt.Sprintf("sha-reserved-%d", i),
			Author:      "alice",
			CommittedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := st.SaveChange(c); err != nil {
			t.Fatalf("SaveChange(%q): %v", name, err)
		}
		i++
	}

	opts, err := st.FacetOptions()
	if err != nil {
		t.Fatalf("FacetOptions: %v", err)
	}

	for name := range reservedChangesetsParams {
		t.Run(name, func(t *testing.T) {
			if vals, ok := opts[name]; ok {
				t.Errorf("FacetOptions returned reserved param %q (from web.reservedChangesetsParams) as a facet key with values %v — store.reservedFacetNames has drifted out of sync", name, vals)
			}
		})
	}

	if _, ok := opts["region"]; !ok {
		t.Error(`FacetOptions missing non-reserved key "region" — exclusion must not affect non-reserved facets`)
	}
}
