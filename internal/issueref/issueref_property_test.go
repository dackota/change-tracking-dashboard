// issueref_property_test.go asserts Parse's operational invariants over
// generated + adversarial input (empty, huge, malformed, arbitrary unicode)
// rather than only the hand-picked examples in issueref_test.go: Parse must
// never panic on any input, every reference it returns must actually occur
// verbatim in the input, and the returned slice must never contain a
// duplicate entry.
package issueref_test

import (
	"strings"
	"testing"
	"testing/quick"

	"github.com/dackota/change-tracking-dashboard/internal/issueref"
)

// TestParse_NeverPanicsAndReturnsOnlyVerbatimDeduplicatedSubstrings_Property
// runs Parse against randomized strings (quick.Check's default generator,
// which includes empty strings, oversized strings, and arbitrary/malformed
// unicode) and checks the invariant holds for every one of them.
func TestParse_NeverPanicsAndReturnsOnlyVerbatimDeduplicatedSubstrings_Property(t *testing.T) {
	t.Parallel()

	invariant := func(text string) bool {
		refs := issueref.Parse(text)

		seen := make(map[string]struct{}, len(refs))
		for _, ref := range refs {
			// Every reported reference must be an exact substring of the input.
			if !strings.Contains(text, ref) {
				return false
			}
			// No duplicates.
			if _, dup := seen[ref]; dup {
				return false
			}
			seen[ref] = struct{}{}
		}
		return true
	}

	// A bare quick.Check call is itself the "never panics" half of the
	// invariant: a panic inside invariant (or Parse) fails the test outright,
	// with no recover needed.
	if err := quick.Check(invariant, &quick.Config{MaxCount: 2000}); err != nil {
		t.Errorf("Parse invariant violated: %v", err)
	}
}

// TestParse_AdversarialInputs_NeverPanics covers specific adversarial shapes
// quick.Check's random generator is unlikely to hit reliably by chance: a
// very long digit run, a very long uppercase-letter run, and input built
// entirely of the pattern's own special characters.
func TestParse_AdversarialInputs_NeverPanics(t *testing.T) {
	t.Parallel()

	adversarial := []string{
		"",
		"#" + strings.Repeat("9", 100_000),
		"A" + strings.Repeat("B", 100_000) + "-" + strings.Repeat("1", 100_000),
		strings.Repeat("#-", 50_000),
		strings.Repeat("ABC-123 ", 10_000),
	}

	for _, text := range adversarial {
		text := text
		t.Run("", func(t *testing.T) {
			t.Parallel()
			_ = issueref.Parse(text) // must not panic
		})
	}
}
