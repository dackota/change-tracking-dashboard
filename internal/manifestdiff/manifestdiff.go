// Package manifestdiff computes the unified line diff and summary between two
// normalized manifest sets — the pure, data-in/data-out core of a Chart diff
// (see CONTEXT.md). It pairs manifests by identity (Kind, Namespace, Name),
// not by position, so a reordered-but-equal manifest set produces no
// spurious diff; a manifest present on only one side is an add or a remove.
//
// This module is pure — no network, filesystem, exec, goroutines, or
// package-level mutable state — and deliberately does not import
// chartrender, so the heavy Helm SDK dependency chartrender pulls in stays
// contained to that package (ADR 0002, PRD user story 19). Manifest is a
// small standalone type carrying the same four fields as
// chartrender.Manifest so a downstream orchestrator (chartdiff) can
// trivially map between the two.
//
// Diff never parses or interprets manifest YAML; it treats it as opaque
// text. The manifest content is untrusted tenant input, so Diff does no HTML
// escaping either — that is the downstream web slice's responsibility.
package manifestdiff

// DefaultMaxUnifiedBytes is the default output size ceiling applied when
// Params.MaxUnifiedBytes is unset (zero). It bounds the emitted Unified diff
// text; Summary counts always reflect the true totals regardless of
// truncation.
const DefaultMaxUnifiedBytes = 256 * 1024 // 256 KiB

// Manifest is a single Kubernetes object from a normalized manifest set —
// the same shape chartrender.Manifest produces, kept as an independent type
// so this package never imports chartrender.
type Manifest struct {
	Kind      string
	Namespace string
	Name      string
	YAML      string
}

// Params is the input to Diff. A params struct is used (rather than
// positional arguments) so future options — e.g. a hunk-context size — can
// be added without breaking callers.
type Params struct {
	// Old and New are the two manifest sets to compare. Diff pairs them by
	// identity (Kind, Namespace, Name), not position; it never mutates the
	// caller's slices.
	Old []Manifest
	New []Manifest
	// MaxUnifiedBytes bounds the emitted Unified diff text. Zero or negative
	// means DefaultMaxUnifiedBytes.
	MaxUnifiedBytes int
}

// Summary is the blast-radius overview shown above a Chart diff's unified
// text.
type Summary struct {
	// ManifestsChanged is the count of manifests whose YAML differs plus
	// manifests added plus manifests removed.
	ManifestsChanged int
	// LinesAdded and LinesRemoved are the TRUE total +/- line counts across
	// the full comparison, computed before any truncation — so the summary
	// stays an honest blast-radius indicator even when Unified is cut short.
	LinesAdded   int
	LinesRemoved int
}

// Result is the outcome of a Diff call.
type Result struct {
	// Unified is the rendered diff: a "+"-prefixed line for each addition, a
	// "-"-prefixed line for each removal, and a " "-prefixed (context) line
	// for everything unchanged — the familiar git diff / helm diff style.
	// It is truncated (at a line boundary) when it would otherwise exceed
	// the configured size ceiling.
	Unified string
	// Summary carries the true totals; see Summary's field docs.
	Summary Summary
	// Truncated is true when Unified was cut short by the size ceiling.
	Truncated bool
}

// Diff compares p.Old and p.New — two normalized manifest sets — and returns
// their unified line diff plus a summary. Diff is pure and deterministic: the
// same inputs always produce the same Result, and neither input slice is
// mutated.
func Diff(p Params) Result {
	maxBytes := p.MaxUnifiedBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxUnifiedBytes
	}

	pairs := pairManifests(p.Old, p.New)

	manifestsChanged := countChangedManifests(pairs)
	unified, added, removed := renderPairs(pairs)

	truncated := false
	if len(unified) > maxBytes {
		unified = truncateAtLineBoundary(unified, maxBytes)
		truncated = true
	}

	return Result{
		Unified: unified,
		Summary: Summary{
			ManifestsChanged: manifestsChanged,
			LinesAdded:       added,
			LinesRemoved:     removed,
		},
		Truncated: truncated,
	}
}
