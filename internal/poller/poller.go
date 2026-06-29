// Package poller orchestrates a single polling cycle for one Tracker:
// it asks the Git source for new commits since the high-water mark, runs
// Extractor → Differ across consecutive file snapshots, attaches facets,
// and persists resulting Changes + the new high-water mark via the Store.
//
// The Poller is a thin coordinator — it delegates all logic to the pure modules
// (extractor, differ, facet) and the I/O edges (gitsource, store).
//
// TODO (backfill-and-poll-config task): add configurable backfill window and
// poll cadence. Currently each Poll() call processes all commits since HWM.
package poller

import (
	"fmt"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/differ"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/extractor"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/facet"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// Poller wires the git source and store together to run polling cycles.
type Poller struct {
	src *gitsource.Source
	st  *store.Store
}

// New returns a Poller wired to the given source and store.
func New(src *gitsource.Source, st *store.Store) *Poller {
	return &Poller{src: src, st: st}
}

// Poll runs one polling cycle for the given Tracker:
//  1. Read the current high-water-mark SHA for the repo.
//  2. Walk commits touching Tracker.FileGlob (treated as a literal path in
//     this skeleton; glob fan-out is the keyed-map-diff task's concern).
//  3. For each consecutive pair of snapshots, extract → diff → attach facets.
//  4. Persist all resulting Changes and update the high-water mark.
func (p *Poller) Poll(t domain.Tracker) error {
	hwm, err := p.st.GetHighWaterMark(t.Repo)
	if err != nil {
		return fmt.Errorf("poller: get HWM for %q: %w", t.Repo, err)
	}

	// FileGlob is used as a literal path in this skeleton.
	// TODO (glob fan-out): expand the glob across the repo tree.
	snapshots, err := p.src.WalkCommits(t.FileGlob, hwm)
	if err != nil {
		return fmt.Errorf("poller: walk commits: %w", err)
	}

	if len(snapshots) == 0 {
		return nil // nothing new since last poll
	}

	ex, err := extractor.New(t.ExtractorExpr)
	if err != nil {
		return fmt.Errorf("poller: compile extractor %q: %w", t.ExtractorExpr, err)
	}

	fe, err := facet.NewExtractor(t.FacetPattern)
	if err != nil {
		return fmt.Errorf("poller: compile facet pattern %q: %w", t.FacetPattern, err)
	}

	// We need a "before" snapshot to diff against. When there is no HWM yet
	// (first run), we treat the state before the oldest snapshot as absent.
	var prevField domain.TrackedField
	if hwm == "" && len(snapshots) > 0 {
		// Extract state of the very first snapshot as the initial "old" value.
		// Then walk pairs starting from index 1 using the first as old.
		// This means: if there's only one snapshot, we produce an "added" Change.
		prevField, err = ex.Extract(snapshots[0].Content)
		if err != nil {
			return fmt.Errorf("poller: extract (initial): %w", err)
		}
		if len(snapshots) == 1 {
			// Only one commit ever — treat absent→first commit as "added".
			facets := fe.ExtractFacets(snapshots[0].FilePath)
			params := differ.ScalarParams{
				Repo:        t.Repo,
				FilePath:    snapshots[0].FilePath,
				Field:       t.Field,
				CommitSha:   snapshots[0].CommitSha,
				Author:      snapshots[0].Author,
				CommittedAt: snapshots[0].CommittedAt,
				Facets:      facets,
			}
			changes := differ.DiffScalar(params, domain.TrackedField{Present: false}, prevField)
			for _, c := range changes {
				if err := p.st.SaveChange(c); err != nil {
					return fmt.Errorf("poller: save change: %w", err)
				}
			}
			return p.st.SetHighWaterMark(t.Repo, snapshots[0].CommitSha)
		}
		snapshots = snapshots[1:]
	} else if hwm != "" {
		// There IS a previous snapshot already processed. We need the file
		// state at the HWM commit to compute the diff for the first new commit.
		// Fetch the snapshot content at the HWM SHA.
		hwmSnaps, err := p.src.WalkCommits(t.FileGlob, "")
		if err != nil {
			return fmt.Errorf("poller: reload all commits for HWM lookup: %w", err)
		}
		for _, snap := range hwmSnaps {
			if snap.CommitSha == hwm {
				prevField, err = ex.Extract(snap.Content)
				if err != nil {
					return fmt.Errorf("poller: extract HWM content: %w", err)
				}
				break
			}
		}
	}

	var lastSha string
	for _, snap := range snapshots {
		newField, err := ex.Extract(snap.Content)
		if err != nil {
			return fmt.Errorf("poller: extract at %s: %w", snap.CommitSha, err)
		}

		facets := fe.ExtractFacets(snap.FilePath)
		params := differ.ScalarParams{
			Repo:        t.Repo,
			FilePath:    snap.FilePath,
			Field:       t.Field,
			CommitSha:   snap.CommitSha,
			Author:      snap.Author,
			CommittedAt: snap.CommittedAt,
			Facets:      facets,
		}

		changes := differ.DiffScalar(params, prevField, newField)
		for _, c := range changes {
			if err := p.st.SaveChange(c); err != nil {
				return fmt.Errorf("poller: save change: %w", err)
			}
		}

		prevField = newField
		lastSha = snap.CommitSha
	}

	if lastSha != "" {
		if err := p.st.SetHighWaterMark(t.Repo, lastSha); err != nil {
			return fmt.Errorf("poller: set HWM: %w", err)
		}
	}

	return nil
}
