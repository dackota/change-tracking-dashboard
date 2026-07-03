package manifestdiff

import (
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

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
// hard byte cut.
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
	return cut
}
