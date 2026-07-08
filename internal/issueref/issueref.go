// Package issueref parses issue/PR references out of free-form text, such as
// a commit message. This module is pure — no I/O, no tracker lookups (that is
// explicitly out of scope; see the tf-issue-correlation task) — it only
// recognizes and returns the reference strings, best-effort.
package issueref

import "regexp"

// refPattern recognizes two reference styles, matched left-to-right over the
// input:
//
//   - GitHub-style: "#" followed by 1-10 digits (e.g. "#123"). The trailing
//     \b means a digit run immediately followed by another word character
//     (e.g. "#123abc") is rejected outright — it is a substring of some
//     larger alphanumeric token, not a standalone reference. Digit runs are
//     capped at 10, so a large numeric-looking substring can never be
//     captured as if it were a real issue number.
//   - Jira-style: an uppercase project key (an uppercase letter followed by
//     1-9 further uppercase letters/digits, so 2-10 characters total)
//     immediately followed by "-" and 1-10 digits (e.g. "ABC-456"). Leading
//     and trailing \b anchors mean the whole key-number pair must stand on
//     its own token — neither preceded nor followed by another word
//     character — so it is never matched inside a larger identifier (e.g.
//     "xABC-456") or a lowercase dash-number token (e.g. "e2-standard-4",
//     which starts with a lowercase letter and so never matches the
//     uppercase-only key class).
//
// Both alternatives are bounded in length as a hardening measure: unbounded
// digit/letter runs on untrusted commit-message text could otherwise let a
// pathological message force a very large or ambiguous "reference" to be
// reported.
var refPattern = regexp.MustCompile(`#[0-9]{1,10}\b|\b[A-Z][A-Z0-9]{1,9}-[0-9]{1,10}\b`)

// Parse scans text (typically a commit message) and returns every distinct
// issue/PR reference found, in the order each first appears. Duplicate
// references — the exact same matched text occurring more than once — are
// reported once, at their first occurrence: a repeated mention of the same
// issue carries no additional linking information beyond the first. Text
// with no reference returns nil (never an error) — Parse cannot fail and
// never panics, regardless of input (empty, oversized, or malformed).
func Parse(text string) []string {
	matches := refPattern.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	refs := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		refs = append(refs, m)
	}
	return refs
}

// Copy returns a defensive copy of refs, safe to embed in a value handed to
// a caller that must never be able to mutate the original slice (mirroring
// the map-copy convention already used for a Change's Facets). A nil/empty
// input copies to nil, preserving "no reference" rather than manufacturing a
// spurious non-nil empty slice — the same convention Parse itself follows.
func Copy(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, len(refs))
	copy(out, refs)
	return out
}
