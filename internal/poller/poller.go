// Package poller orchestrates a single polling cycle for one Tracker:
// it asks the Git source for new commits since the high-water mark, runs
// Extractor → Differ across consecutive file snapshots, attaches facets,
// and persists resulting Changes + the new high-water mark via the Store.
//
// The Poller is a thin coordinator — it delegates all logic to the pure modules
// (extractor, differ, facet) and the I/O edges (gitsource, store).
//
// On first run (HWM empty), the walk is bounded to the BackfillDays window
// configured on the Tracker. An injectable clock (WithNow) enables deterministic
// testing against fixture repos with fixed commit dates.
package poller

import (
	"fmt"
	"strings"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/differ"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/extractor"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/facet"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/gitsource"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// diffFields dispatches to DiffKeyed or DiffScalar based on whether either
// TrackedField is a keyed map result. If either old or new is keyed, both are
// treated as keyed (a nil Map is equivalent to an empty map for keyed diffing).
// This means the poller does not need explicit kind configuration on the Tracker
// — the extractor's output type determines the diff path automatically.
func diffFields(p differ.ScalarParams, old, new domain.TrackedField) []domain.Change {
	if old.IsKeyed() || new.IsKeyed() {
		return differ.DiffKeyed(p, old, new)
	}
	return differ.DiffScalar(p, old, new)
}

// Poller wires the git source and store together to run polling cycles.
type Poller struct {
	src *gitsource.Source
	st  *store.Store
	// now returns the current wall time. Defaults to time.Now; tests may inject
	// a fixed clock to make the backfill window deterministic.
	now func() time.Time
}

// New returns a Poller wired to the given source and store.
func New(src *gitsource.Source, st *store.Store) *Poller {
	return &Poller{src: src, st: st, now: time.Now}
}

// WithNow returns a copy of the Poller with a custom clock function. It is
// intended for tests that need a deterministic reference point for the backfill
// window calculation.
func (p *Poller) WithNow(fn func() time.Time) *Poller {
	return &Poller{src: p.src, st: p.st, now: fn}
}

// globMetaChars are the path.Match wildcard characters. A FileGlob containing
// any of these is fanned out across the repo tree; one with none of them is a
// literal path and is walked directly (no enumeration), preserving prior
// behavior exactly.
const globMetaChars = "*?["

// isGlob reports whether pattern contains any path.Match wildcard metacharacter.
func isGlob(pattern string) bool {
	return strings.ContainsAny(pattern, globMetaChars)
}

// Poll runs one polling cycle for the given Tracker:
//  1. Resolve Tracker.FileGlob to the set of concrete file paths to walk: a
//     literal path resolves to itself; a wildcard glob is expanded against the
//     repo's HEAD tree via gitsource.MatchingFiles.
//  2. For each resolved file path, independently: read its own high-water-mark
//     (keyed by repo+path so fanned-out files never share a cursor), walk its
//     commit history, and run Extractor → Differ → facet attachment exactly as
//     for a single tracked file. On first run (HWM empty) the walk is bounded
//     by the backfill window (Tracker.BackfillDays days before now).
//  3. Persist all resulting Changes and each file's high-water mark.
func (p *Poller) Poll(t domain.Tracker) error {
	ex, err := extractor.New(t.ExtractorExpr)
	if err != nil {
		return fmt.Errorf("poller: compile extractor %q: %w", t.ExtractorExpr, err)
	}

	fe, err := facet.NewExtractor(t.FacetPattern)
	if err != nil {
		return fmt.Errorf("poller: compile facet pattern %q: %w", t.FacetPattern, err)
	}

	filePaths, err := p.resolveFilePaths(t.FileGlob)
	if err != nil {
		return fmt.Errorf("poller: resolve file glob %q: %w", t.FileGlob, err)
	}

	for _, filePath := range filePaths {
		if err := p.pollFile(t, filePath, ex, fe); err != nil {
			return err
		}
	}

	return nil
}

// resolveFilePaths expands glob into the concrete file paths to walk. A
// literal path (no wildcard metacharacters) resolves to itself unconditionally
// — even if the file doesn't exist at HEAD — preserving the pre-fan-out
// behavior where WalkCommits is simply attempted against the literal path. A
// wildcard glob is expanded against the repo's HEAD tree.
func (p *Poller) resolveFilePaths(glob string) ([]string, error) {
	if !isGlob(glob) {
		return []string{glob}, nil
	}
	return p.src.MatchingFiles(glob)
}

// pollFile runs one polling cycle for a single concrete file path: read its
// own HWM, walk its commit history (bounded by the backfill window on first
// run), diff consecutive snapshots, attach facets from this file's own path,
// and persist Changes plus the file's new HWM.
func (p *Poller) pollFile(t domain.Tracker, filePath string, ex *extractor.Extractor, fe *facet.Extractor) error {
	hwm, err := p.st.GetHighWaterMark(t.Repo, filePath)
	if err != nil {
		return fmt.Errorf("poller: get HWM for %q/%q: %w", t.Repo, filePath, err)
	}

	// On first run, bound the walk to the configured backfill window.
	// On incremental runs the HWM already provides the boundary; no time bound.
	var notBefore time.Time
	if hwm == "" && t.BackfillDays >= 0 {
		notBefore = p.now().Add(-time.Duration(t.BackfillDays) * 24 * time.Hour)
	}

	snapshots, err := p.src.WalkCommits(filePath, hwm, notBefore)
	if err != nil {
		return fmt.Errorf("poller: walk commits for %q: %w", filePath, err)
	}

	if len(snapshots) == 0 {
		return nil // nothing new since last poll
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
			changes := diffFields(params, domain.TrackedField{Present: false}, prevField)
			for _, c := range changes {
				if err := p.st.SaveChange(c); err != nil {
					return fmt.Errorf("poller: save change: %w", err)
				}
			}
			return p.st.SetHighWaterMark(t.Repo, filePath, snapshots[0].CommitSha)
		}
		snapshots = snapshots[1:]
	} else if hwm != "" {
		// There IS a previous snapshot already processed. We need the file
		// state at the HWM commit to compute the diff for the first new commit.
		// This lookup MUST be unbounded (zero notBefore) so we can find an HWM
		// commit that may predate the backfill window.
		hwmSnaps, err := p.src.WalkCommits(filePath, "", time.Time{})
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

		changes := diffFields(params, prevField, newField)
		for _, c := range changes {
			if err := p.st.SaveChange(c); err != nil {
				return fmt.Errorf("poller: save change: %w", err)
			}
		}

		prevField = newField
		lastSha = snap.CommitSha
	}

	if lastSha != "" {
		if err := p.st.SetHighWaterMark(t.Repo, filePath, lastSha); err != nil {
			return fmt.Errorf("poller: set HWM: %w", err)
		}
	}

	return nil
}
