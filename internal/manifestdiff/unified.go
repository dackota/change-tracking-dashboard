package manifestdiff

import (
	"strings"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
)

// renderPairs renders the assembled Unified diff across all identity pairs,
// in the sorted order pairManifests produced, and returns the true total
// added/removed line counts. Each pair's diff is fully self-contained — a
// pair contributes nothing at all when its YAML is identical on both sides,
// so identical or reordered-but-equal manifest sets never produce a spurious
// line. Pairs are concatenated directly with no separator token between
// them, so no artificial boundary line can ever enter the diffed content or
// be miscounted as an addition or removal.
//
// Every block renderPairs concatenates is already guaranteed to end in "\n"
// (see writeDiffLine): each one is built entirely out of writeDiffLine
// calls, and that guarantee holds regardless of whether the manifest's own
// (caller-supplied, unvalidated) YAML ended in a newline. So blocks can
// simply be concatenated here with no additional boundary handling.
func renderPairs(pairs []pair) (unified string, added, removed int) {
	var b strings.Builder
	totalAdded, totalRemoved := 0, 0

	for _, p := range pairs {
		var block string
		var a, r int

		switch {
		case p.inOld && p.inNew:
			if p.oldYAML == p.newYAML {
				continue // identical manifest: no diff content at all
			}
			block, a, r = lineDiff(p.oldYAML, p.newYAML)

		case p.inOld: // removed: every line of the old YAML is a "-" line
			block, r = renderWhole(p.oldYAML, "-")

		case p.inNew: // added: every line of the new YAML is a "+" line
			block, a = renderWhole(p.newYAML, "+")
		}

		b.WriteString(block)
		totalAdded += a
		totalRemoved += r
	}

	return b.String(), totalAdded, totalRemoved
}

// writeDiffLine writes exactly one logical diff line to b: prefix, then
// line, then a "\n" terminator if line doesn't already end in one.
//
// This is the single chokepoint every prefixed diff line in this package
// passes through — renderUnified's insert/delete/equal loop and renderWhole
// both call it for every line they emit. A manifest's YAML is
// caller-supplied, unvalidated text and is not guaranteed to end in "\n";
// enforcing the terminator here, at the moment each line is written, means
// no line — whichever diff op or manifest it came from — can ever be left
// unterminated for a subsequent write to glue onto. The appended terminator
// is never itself counted as an added or removed line.
func writeDiffLine(b *strings.Builder, prefix, line string) {
	b.WriteString(prefix)
	b.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		b.WriteString("\n")
	}
}

// renderWhole prefixes every line of text with prefix (e.g. "+" or "-") and
// returns the rendered block plus the number of lines it contains. It is
// used for a manifest present on only one side, where there is no
// counterpart to line-diff against.
func renderWhole(text, prefix string) (block string, lineCount int) {
	var b strings.Builder
	for _, line := range splitPreservingNewlines(text) {
		writeDiffLine(&b, prefix, line)
		lineCount++
	}
	return b.String(), lineCount
}

// lineDiff computes the line-level diff between oldText and newText with
// go-udiff (udiff.Lines aligns the edit script to whole-line boundaries),
// then renders it as a unified +/- diff and returns the true added/removed
// line counts.
//
// When the two texts are identical, lineDiff short-circuits to an empty
// result rather than rendering the whole text as context: a "diff" with
// nothing to show is empty, the same way `git diff` prints nothing for an
// unmodified tree.
func lineDiff(oldText, newText string) (unified string, added, removed int) {
	if oldText == newText {
		return "", 0, 0
	}

	edits := udiff.Lines(oldText, newText)
	// ToUnifiedDiff includes up to contextLines of unchanged lines around each
	// change. Sizing the context to the combined line count guarantees every
	// unchanged line is within reach of some change, so the whole diff renders
	// as a single full-context hunk (no @@ headers, no elided regions) — the
	// rendering this package has always produced. It is bounded to the actual
	// text size rather than a huge constant because ToUnifiedDiff does work
	// proportional to the context value.
	context := strings.Count(oldText, "\n") + strings.Count(newText, "\n") + 2
	diff, err := udiff.ToUnifiedDiff("old", "new", oldText, edits, context)
	if err != nil {
		// ToUnifiedDiff only errors when an edit is out of bounds for content,
		// which udiff.Lines never produces from oldText itself. Fall back to a
		// whole-file replacement so a changed pair still yields a diff rather
		// than silently vanishing.
		removedBlock, r := renderWhole(oldText, "-")
		addedBlock, a := renderWhole(newText, "+")
		return removedBlock + addedBlock, a, r
	}

	return renderUnified(diff)
}

// renderUnified turns go-udiff's per-line ops into a unified +/- diff: each
// inserted line is "+"-prefixed, each deleted line is "-"-prefixed, and each
// equal line is " "-prefixed context — the familiar git diff / helm diff
// style.
//
// Every line goes through writeDiffLine, so a line whose Content lacks a
// trailing newline (whichever side — old or new — didn't end in one) is
// terminated before the next line is written and can never fuse onto it,
// regardless of the order go-udiff emits deletes and inserts in.
func renderUnified(diff udiff.UnifiedDiff) (unified string, added, removed int) {
	var b strings.Builder
	for _, h := range diff.Hunks {
		for _, ln := range h.Lines {
			prefix := " " // udiff.Equal: context
			switch ln.Kind {
			case udiff.Insert:
				prefix = "+"
				added++
			case udiff.Delete:
				prefix = "-"
				removed++
			}
			writeDiffLine(&b, prefix, ln.Content)
		}
	}
	return b.String(), added, removed
}

// splitPreservingNewlines splits s into lines, each retaining its trailing
// "\n" (except possibly the last, if s doesn't end in one), so re-joining
// prefixed lines reconstructs the original line structure exactly.
func splitPreservingNewlines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	// SplitAfter leaves a trailing "" element when s ends in "\n"; drop it
	// so callers don't emit a spurious empty prefixed line.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// truncateAtLineBoundary cuts s to at most maxBytes bytes, backing up to the
// preceding newline so the result never ends mid-line. If no newline exists
// within the bound (a single line longer than maxBytes), it falls back to a
// hard byte cut backed up to a valid UTF-8 rune boundary, so a truncated
// result is never invalid UTF-8.
func truncateAtLineBoundary(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
		return cut[:i+1]
	}
	return truncateToValidUTF8(cut)
}

// truncateToValidUTF8 backs cut up, one byte at a time, until it is valid
// UTF-8 — undoing a hard byte-level cut that landed mid-rune. A multi-byte
// UTF-8 rune is at most 4 bytes, so this trims at most a few bytes off the
// tail.
func truncateToValidUTF8(cut string) string {
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
