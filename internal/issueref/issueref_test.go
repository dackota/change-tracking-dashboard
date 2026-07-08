// Package issueref_test exercises Parse's public behavior only — never the
// underlying regex internals — against representative commit-message text.
package issueref_test

import (
	"reflect"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/issueref"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "bare numeric GitHub-style reference",
			text: "Fixes #123",
			want: []string{"#123"},
		},
		{
			name: "project-key Jira-style reference",
			text: "See ABC-456 for context",
			want: []string{"ABC-456"},
		},
		{
			name: "multiple distinct references of both styles in one message",
			text: "Fixes #123 and ABC-456, also touches #789",
			want: []string{"#123", "ABC-456", "#789"},
		},
		{
			name: "no reference present",
			text: "chore: bump provider version",
			want: nil,
		},
		{
			name: "empty message",
			text: "",
			want: nil,
		},
		{
			name: "duplicate references are reported once at first occurrence",
			text: "Fixes #123, closes #123 again, and ABC-456 / ABC-456",
			want: []string{"#123", "ABC-456"},
		},
		{
			name: "numeric reference embedded in a longer alphanumeric token is not a reference",
			text: "See commit #123abc for the prior attempt",
			want: nil,
		},
		{
			name: "Jira-style key embedded in a larger token is not a reference, but a standalone one still is",
			text: "prefixed xABC-456 should not match, but standalone ABC-456 should",
			want: []string{"ABC-456"},
		},
		{
			name: "lowercase-prefixed dash-number (e.g. a machine type) is not a Jira-style reference",
			text: "resize node pool to e2-standard-4",
			want: nil,
		},
		{
			name: "a bare hex-looking commit hash is not a reference",
			text: "reverts abcdef1234567",
			want: nil,
		},
		{
			name: "a semver-style version string is not a reference",
			text: "bump to v1.2.3-4",
			want: nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := issueref.Parse(tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q) = %#v, want %#v", tc.text, got, tc.want)
			}
		})
	}
}

// TestCopy verifies Copy's defensive-copy contract: the returned slice holds
// the same values but is never an alias of the input (mutating one must not
// affect the other), and a nil/empty input copies to nil rather than a
// spurious non-nil empty slice.
func TestCopy(t *testing.T) {
	t.Parallel()

	t.Run("nil input copies to nil", func(t *testing.T) {
		t.Parallel()
		if got := issueref.Copy(nil); got != nil {
			t.Errorf("Copy(nil) = %#v, want nil", got)
		}
	})

	t.Run("empty input copies to nil", func(t *testing.T) {
		t.Parallel()
		if got := issueref.Copy([]string{}); got != nil {
			t.Errorf("Copy([]string{}) = %#v, want nil", got)
		}
	})

	t.Run("non-empty input copies values and is independent of the source", func(t *testing.T) {
		t.Parallel()
		src := []string{"#123", "ABC-456"}
		got := issueref.Copy(src)
		if !reflect.DeepEqual(got, src) {
			t.Fatalf("Copy(%#v) = %#v, want equal values", src, got)
		}
		got[0] = "mutated"
		if src[0] != "#123" {
			t.Errorf("mutating Copy's result leaked back into source: %#v", src)
		}
	})
}
