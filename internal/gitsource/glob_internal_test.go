package gitsource

import (
	"strings"
	"testing"
)

// TestGlobToRegexp covers the ** cross-segment translation, single-segment
// wildcards inside a ** glob, character-class passthrough, metacharacter
// escaping, and anchoring. globToRegexp is only used for globs containing "**"
// (MatchingFiles routes the rest through path.Match), but its contract is
// exercised directly here.
func TestGlobToRegexp(t *testing.T) {
	cases := []struct {
		glob string
		path string
		want bool
	}{
		// ** matches zero or more path segments (the "**/" form matches none too).
		{"gitops/**/Chart.yaml", "gitops/a/Chart.yaml", true},
		{"gitops/**/Chart.yaml", "gitops/a/b/Chart.yaml", true},
		{"gitops/**/Chart.yaml", "gitops/Chart.yaml", true},
		{"gitops/**/Chart.yaml", "other/a/Chart.yaml", false},
		{"gitops/**/Chart.yaml", "gitops/a/values.yaml", false},
		{"**/values.yaml", "values.yaml", true},
		{"**/values.yaml", "a/b/values.yaml", true},

		// Anchoring: no partial prefix/suffix matches.
		{"gitops/**/Chart.yaml", "xgitops/a/Chart.yaml", false},
		{"gitops/**/Chart.yaml", "gitops/a/Chart.yamlx", false},

		// A single "*" inside a ** glob stays within one segment.
		{"gitops/**/v*.yaml", "gitops/a/values.yaml", true},
		{"gitops/**/v*.yaml", "gitops/a/b/vx.yaml", true},
		{"gitops/**/*.yaml", "gitops/a/x.yaml", true},
		{"gitops/**/*.yaml", "gitops/a/x.yml", false},

		// "?" matches exactly one non-separator char.
		{"gitops/**/Chart.yam?", "gitops/a/Chart.yaml", true},
		{"gitops/**/Chart.yam?", "gitops/a/Chart.yamls", false},

		// Character classes pass through (this is the regression the review caught).
		{"gitops/**/values-[a-z].yaml", "gitops/a/values-x.yaml", true},
		{"gitops/**/values-[a-z].yaml", "gitops/values-b.yaml", true},
		{"gitops/**/values-[a-z].yaml", "gitops/a/values-1.yaml", false},
		// Negated class ("^").
		{"gitops/**/[^_]*.yaml", "gitops/a/values.yaml", true},
		{"gitops/**/[^_]*.yaml", "gitops/a/_hidden.yaml", false},

		// Metacharacters are escaped: the dots are literal.
		{"gitops/**/a.b", "gitops/x/a.b", true},
		{"gitops/**/a.b", "gitops/x/aXb", false},

		// Unterminated "[" degrades to a literal "[" rather than erroring.
		{"a/**/x[", "a/q/x[", true},
	}

	for _, c := range cases {
		re, err := globToRegexp(c.glob)
		if err != nil {
			t.Errorf("globToRegexp(%q) unexpected error: %v", c.glob, err)
			continue
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("globToRegexp(%q)=%q; MatchString(%q)=%v, want %v",
				c.glob, re.String(), c.path, got, c.want)
		}
	}
}

// FuzzGlobToRegexp asserts the operational invariant that globToRegexp never
// panics on any input and, when it returns a regexp, that regexp is usable and
// anchored (^…$). An invalid character-class range is allowed to surface as a
// compile error (mirroring path.Match rejecting a bad pattern) — it just must
// not panic.
func FuzzGlobToRegexp(f *testing.F) {
	for _, seed := range []string{
		"gitops/**/Chart.yaml", "**", "**/", "a/**/b", "a**b", "[a-z]",
		"gitops/**/[^_]*.yaml", "a[b", "[]a]", "*.*", "???", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, glob string) {
		re, err := globToRegexp(glob) // must not panic for any input
		if err != nil {
			return // an uncompilable class range is acceptable, not a crash
		}
		if s := re.String(); !strings.HasPrefix(s, "^") || !strings.HasSuffix(s, "$") {
			t.Errorf("globToRegexp(%q) not anchored: %q", glob, s)
		}
		_ = re.MatchString("gitops/a/b/Chart.yaml") // must not panic
	})
}
