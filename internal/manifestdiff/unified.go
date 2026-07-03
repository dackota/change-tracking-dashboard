package manifestdiff

import (
	"strings"
	"unicode/utf8"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// renderPairs renders the assembled Unified diff across all identity pairs,
// in the sorted order pairManifests produced, and returns the true total
// added/removed line counts. Each pair's diff is fully self-contained — a
// pair contributes nothing at all when its YAML is identical on both sides,
// so identical or reordered-but-equal manifest sets never produce a spurious
// line. Pairs are concatenated directly with no separator token between
// them, so no artificial boundary line can ever enter the diffed content or
// be miscounted as an addition or removal.
func renderPairs(pairs []pair) (unified string, added, removed int) {
	var b strings.Builder
	totalAdded, totalRemoved := 0, 0

	for _, p := range pairs {
		switch {
		case p.inOld && p.inNew:
			if p.oldYAML == p.newYAML {
				continue // identical manifest: no diff content at all
			}
			block, a, r := lineDiff(p.oldYAML, p.newYAML)
			b.WriteString(block)
			totalAdded += a
			totalRemoved += r

		case p.inOld: // removed: every line of the old YAML is a "-" line
			block, r := renderWhole(p.oldYAML, "-")
			b.WriteString(block)
			totalRemoved += r

		case p.inNew: // added: every line of the new YAML is a "+" line
			block, a := renderWhole(p.newYAML, "+")
			b.WriteString(block)
			totalAdded += a
		}
	}

	return b.String(), totalAdded, totalRemoved
}

// renderWhole prefixes every line of text with prefix (e.g. "+" or "-") and
// returns the rendered block plus the number of lines it contains. It is
// used for a manifest present on only one side, where there is no
// counterpart to line-diff against.
func renderWhole(text, prefix string) (block string, lineCount int) {
	var b strings.Builder
	for _, line := range splitPreservingNewlines(text) {
		b.WriteString(prefix)
		b.WriteString(line)
		lineCount++
	}
	return b.String(), lineCount
}

// lineDiff computes the line-level diff between oldText and newText using
// diffmatchpatch's line-mode technique (DiffLinesToChars → DiffMain →
// DiffCharsToLines: each line is hashed to a single rune so the underlying
// character-level diff algorithm operates on whole lines), then renders it
// as a unified +/- diff and returns the true added/removed line counts.
//
// When the two texts are identical, lineDiff short-circuits to an empty
// result rather than rendering the whole text as context: a "diff" with
// nothing to show is empty, the same way `git diff` prints nothing for an
// unmodified tree.
func lineDiff(oldText, newText string) (unified string, added, removed int) {
	if oldText == newText {
		return "", 0, 0
	}

	dmp := diffmatchpatch.New()

	charsOld, charsNew, lineArray := dmp.DiffLinesToChars(oldText, newText)
	diffs := dmp.DiffMain(charsOld, charsNew, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)

	return renderUnified(diffs)
}

// renderUnified turns diffmatchpatch's insert/delete/equal ops into a
// unified +/- diff: each line of an insert op is "+"-prefixed, each line of
// a delete op is "-"-prefixed, and each line of an equal op is " "-prefixed
// context — the familiar git diff / helm diff style.
func renderUnified(diffs []diffmatchpatch.Diff) (unified string, added, removed int) {
	var b strings.Builder
	for _, d := range diffs {
		for _, line := range splitPreservingNewlines(d.Text) {
			switch d.Type {
			case diffmatchpatch.DiffInsert:
				b.WriteString("+")
				b.WriteString(line)
				added++
			case diffmatchpatch.DiffDelete:
				b.WriteString("-")
				b.WriteString(line)
				removed++
			default: // diffmatchpatch.DiffEqual
				b.WriteString(" ")
				b.WriteString(line)
			}
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
