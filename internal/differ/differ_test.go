package differ_test

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/differ"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
)

func ptr(s string) *string { return &s }

func TestDiffScalar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		field      string
		repo       string
		filePath   string
		commitSha  string
		author     string
		old        domain.TrackedField
		new        domain.TrackedField
		wantLen    int
		wantChange *domain.Change // nil when wantLen == 0
	}{
		{
			name:      "value changed produces modified Change",
			field:     "aidp-version",
			repo:      "apps-repo",
			filePath:  "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			commitSha: "abc123",
			author:    "alice",
			old:       domain.TrackedField{Value: "1.0.0", Present: true},
			new:       domain.TrackedField{Value: "1.1.0", Present: true},
			wantLen:   1,
			wantChange: &domain.Change{
				Repo:       "apps-repo",
				FilePath:   "apps/tenant-zero/dev/us-west-2/Chart.yaml",
				Field:      "aidp-version",
				Key:        nil,
				ChangeType: domain.ChangeTypeModified,
				OldValue:   ptr("1.0.0"),
				NewValue:   ptr("1.1.0"),
				CommitSha:  "abc123",
				Author:     "alice",
			},
		},
		{
			name:    "value unchanged produces no Change",
			field:   "aidp-version",
			old:     domain.TrackedField{Value: "1.0.0", Present: true},
			new:     domain.TrackedField{Value: "1.0.0", Present: true},
			wantLen: 0,
		},
		{
			name:    "both absent produces no Change",
			field:   "aidp-version",
			old:     domain.TrackedField{Present: false},
			new:     domain.TrackedField{Present: false},
			wantLen: 0,
		},
		{
			name:      "field newly present produces added Change",
			field:     "aidp-version",
			repo:      "apps-repo",
			filePath:  "Chart.yaml",
			commitSha: "def456",
			author:    "bob",
			old:       domain.TrackedField{Present: false},
			new:       domain.TrackedField{Value: "2.0.0", Present: true},
			wantLen:   1,
			wantChange: &domain.Change{
				Repo:       "apps-repo",
				FilePath:   "Chart.yaml",
				Field:      "aidp-version",
				Key:        nil,
				ChangeType: domain.ChangeTypeAdded,
				OldValue:   nil,
				NewValue:   ptr("2.0.0"),
				CommitSha:  "def456",
				Author:     "bob",
			},
		},
		{
			name:      "field becomes absent produces removed Change",
			field:     "aidp-version",
			repo:      "apps-repo",
			filePath:  "Chart.yaml",
			commitSha: "ghi789",
			author:    "carol",
			old:       domain.TrackedField{Value: "1.0.0", Present: true},
			new:       domain.TrackedField{Present: false},
			wantLen:   1,
			wantChange: &domain.Change{
				Repo:       "apps-repo",
				FilePath:   "Chart.yaml",
				Field:      "aidp-version",
				Key:        nil,
				ChangeType: domain.ChangeTypeRemoved,
				OldValue:   ptr("1.0.0"),
				NewValue:   nil,
				CommitSha:  "ghi789",
				Author:     "carol",
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := differ.ScalarParams{
				Repo:      tc.repo,
				FilePath:  tc.filePath,
				Field:     tc.field,
				CommitSha: tc.commitSha,
				Author:    tc.author,
			}

			changes := differ.DiffScalar(params, tc.old, tc.new)

			if len(changes) != tc.wantLen {
				t.Fatalf("DiffScalar() returned %d changes, want %d", len(changes), tc.wantLen)
			}

			if tc.wantChange == nil {
				return
			}

			got := changes[0]
			if got.Repo != tc.wantChange.Repo {
				t.Errorf("Repo: got %q, want %q", got.Repo, tc.wantChange.Repo)
			}
			if got.FilePath != tc.wantChange.FilePath {
				t.Errorf("FilePath: got %q, want %q", got.FilePath, tc.wantChange.FilePath)
			}
			if got.Field != tc.wantChange.Field {
				t.Errorf("Field: got %q, want %q", got.Field, tc.wantChange.Field)
			}
			if got.Key != nil {
				t.Errorf("Key: got %v, want nil (scalar)", got.Key)
			}
			if got.ChangeType != tc.wantChange.ChangeType {
				t.Errorf("ChangeType: got %q, want %q", got.ChangeType, tc.wantChange.ChangeType)
			}
			if !ptrEqual(got.OldValue, tc.wantChange.OldValue) {
				t.Errorf("OldValue: got %v, want %v", ptrStr(got.OldValue), ptrStr(tc.wantChange.OldValue))
			}
			if !ptrEqual(got.NewValue, tc.wantChange.NewValue) {
				t.Errorf("NewValue: got %v, want %v", ptrStr(got.NewValue), ptrStr(tc.wantChange.NewValue))
			}
			if got.CommitSha != tc.wantChange.CommitSha {
				t.Errorf("CommitSha: got %q, want %q", got.CommitSha, tc.wantChange.CommitSha)
			}
			if got.Author != tc.wantChange.Author {
				t.Errorf("Author: got %q, want %q", got.Author, tc.wantChange.Author)
			}
		})
	}
}

func ptrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func ptrStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// changeKey is a test helper to summarise a Change in a way that is easy to
// index in test assertions (key + changeType).
type changeKey struct {
	key        string // "" if nil
	changeType domain.ChangeType
}

func changeKeyOf(c domain.Change) changeKey {
	k := ""
	if c.Key != nil {
		k = *c.Key
	}
	return changeKey{key: k, changeType: c.ChangeType}
}

// indexByKey returns a map from map-key → Change for easy per-key assertions.
func indexByKey(changes []domain.Change) map[string]domain.Change {
	out := make(map[string]domain.Change, len(changes))
	for _, c := range changes {
		k := ""
		if c.Key != nil {
			k = *c.Key
		}
		out[k] = c
	}
	return out
}

// TestDiffScalar_CopiesIssueRefsFromParamsDefensively proves that a Change
// produced by DiffScalar carries ScalarParams.IssueRefs (the issue/PR
// references parsed from the triggering commit's message — see
// internal/issueref) and that the Change's slice is its own copy, never an
// alias of the caller's params.IssueRefs slice.
func TestDiffScalar_CopiesIssueRefsFromParamsDefensively(t *testing.T) {
	t.Parallel()

	inputRefs := []string{"#123", "ABC-456"}
	params := differ.ScalarParams{
		Repo:      "apps-repo",
		FilePath:  "versions.tf",
		Field:     "google-provider-version",
		CommitSha: "abc123",
		Author:    "alice",
		IssueRefs: inputRefs,
	}

	changes := differ.DiffScalar(params, domain.TrackedField{Present: false}, domain.TrackedField{Value: "1.0.0", Present: true})
	if len(changes) != 1 {
		t.Fatalf("DiffScalar() returned %d changes, want 1", len(changes))
	}

	if got := changes[0].IssueRefs; !reflect.DeepEqual(got, inputRefs) {
		t.Errorf("Change.IssueRefs = %#v, want %#v", got, inputRefs)
	}

	// Mutating the returned Change's slice must not affect the caller's
	// original params.IssueRefs (defensive copy, not aliasing) — mirrors the
	// existing Facets-map defensive-copy contract.
	changes[0].IssueRefs[0] = "mutated"
	if inputRefs[0] != "#123" {
		t.Errorf("mutating Change.IssueRefs leaked back into params.IssueRefs: %#v", inputRefs)
	}
}

// TestDiffScalar_NilIssueRefs_YieldsNilOnChange proves that a commit with no
// parsed issue references (params.IssueRefs is nil, matching issueref.Parse's
// nil return for "no reference found") produces a Change with no false link
// — IssueRefs stays nil/empty, never a spuriously non-empty slice.
func TestDiffScalar_NilIssueRefs_YieldsNilOnChange(t *testing.T) {
	t.Parallel()

	params := differ.ScalarParams{
		Repo:      "apps-repo",
		FilePath:  "versions.tf",
		Field:     "google-provider-version",
		CommitSha: "abc123",
		Author:    "alice",
		IssueRefs: nil,
	}

	changes := differ.DiffScalar(params, domain.TrackedField{Present: false}, domain.TrackedField{Value: "1.0.0", Present: true})
	if len(changes) != 1 {
		t.Fatalf("DiffScalar() returned %d changes, want 1", len(changes))
	}
	if len(changes[0].IssueRefs) != 0 {
		t.Errorf("Change.IssueRefs = %#v, want empty", changes[0].IssueRefs)
	}
}

// TestDiffKeyed exercises all per-key cases: modified, added, removed, unchanged,
// and the mixed case where multiple changes occur simultaneously.
func TestDiffKeyed(t *testing.T) {
	t.Parallel()

	baseTime := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	params := differ.ScalarParams{
		Repo:        "apps-repo",
		FilePath:    "aidp/k8/Chart.yaml",
		Field:       "subchart-versions",
		CommitSha:   "abc123",
		Author:      "alice",
		CommittedAt: baseTime,
		Facets:      map[string]string{"env": "dev"},
	}

	tests := []struct {
		name        string
		old         domain.TrackedField
		new         domain.TrackedField
		wantChanges []domain.Change // use Key+ChangeType as identity
	}{
		{
			name: "modified key produces one modified Change",
			old:  domain.TrackedField{Present: true, Map: map[string]string{"gateway": "0.38.0"}},
			new:  domain.TrackedField{Present: true, Map: map[string]string{"gateway": "0.39.0"}},
			wantChanges: []domain.Change{
				{
					Key:        ptr("gateway"),
					ChangeType: domain.ChangeTypeModified,
					OldValue:   ptr("0.38.0"),
					NewValue:   ptr("0.39.0"),
				},
			},
		},
		{
			name: "new key in new produces added Change",
			old:  domain.TrackedField{Present: true, Map: map[string]string{"gateway": "0.38.0"}},
			new:  domain.TrackedField{Present: true, Map: map[string]string{"gateway": "0.38.0", "engine": "1.0.0"}},
			wantChanges: []domain.Change{
				{
					Key:        ptr("engine"),
					ChangeType: domain.ChangeTypeAdded,
					OldValue:   nil,
					NewValue:   ptr("1.0.0"),
				},
			},
		},
		{
			name: "key removed from new produces removed Change",
			old:  domain.TrackedField{Present: true, Map: map[string]string{"gateway": "0.38.0", "engine": "1.0.0"}},
			new:  domain.TrackedField{Present: true, Map: map[string]string{"gateway": "0.38.0"}},
			wantChanges: []domain.Change{
				{
					Key:        ptr("engine"),
					ChangeType: domain.ChangeTypeRemoved,
					OldValue:   ptr("1.0.0"),
					NewValue:   nil,
				},
			},
		},
		{
			name:        "unchanged key produces no Change",
			old:         domain.TrackedField{Present: true, Map: map[string]string{"gateway": "0.38.0"}},
			new:         domain.TrackedField{Present: true, Map: map[string]string{"gateway": "0.38.0"}},
			wantChanges: nil,
		},
		{
			name: "mixed: one modified, one added, one removed simultaneously",
			old: domain.TrackedField{Present: true, Map: map[string]string{
				"gateway": "0.38.0",
				"engine":  "1.0.0",
				"ui":      "0.5.0",
			}},
			new: domain.TrackedField{Present: true, Map: map[string]string{
				"gateway": "0.39.0", // modified
				// engine removed
				"analytics": "2.0.0", // added
				"ui":        "0.5.0", // unchanged
			}},
			wantChanges: []domain.Change{
				{Key: ptr("gateway"), ChangeType: domain.ChangeTypeModified, OldValue: ptr("0.38.0"), NewValue: ptr("0.39.0")},
				{Key: ptr("engine"), ChangeType: domain.ChangeTypeRemoved, OldValue: ptr("1.0.0"), NewValue: nil},
				{Key: ptr("analytics"), ChangeType: domain.ChangeTypeAdded, OldValue: nil, NewValue: ptr("2.0.0")},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			changes := differ.DiffKeyed(params, tc.old, tc.new)

			if len(changes) != len(tc.wantChanges) {
				t.Fatalf("DiffKeyed() returned %d changes, want %d; got: %+v",
					len(changes), len(tc.wantChanges), changes)
			}

			if len(tc.wantChanges) == 0 {
				return
			}

			// Index by map-key for order-independent assertions.
			got := indexByKey(changes)

			for _, want := range tc.wantChanges {
				k := ""
				if want.Key != nil {
					k = *want.Key
				}
				c, ok := got[k]
				if !ok {
					t.Errorf("missing Change for key %q", k)
					continue
				}
				if c.ChangeType != want.ChangeType {
					t.Errorf("key %q: ChangeType = %q, want %q", k, c.ChangeType, want.ChangeType)
				}
				if !ptrEqual(c.OldValue, want.OldValue) {
					t.Errorf("key %q: OldValue = %s, want %s", k, ptrStr(c.OldValue), ptrStr(want.OldValue))
				}
				if !ptrEqual(c.NewValue, want.NewValue) {
					t.Errorf("key %q: NewValue = %s, want %s", k, ptrStr(c.NewValue), ptrStr(want.NewValue))
				}
				// Metadata must be copied from params.
				if c.Repo != params.Repo {
					t.Errorf("key %q: Repo = %q, want %q", k, c.Repo, params.Repo)
				}
				if c.Field != params.Field {
					t.Errorf("key %q: Field = %q, want %q", k, c.Field, params.Field)
				}
				if c.CommitSha != params.CommitSha {
					t.Errorf("key %q: CommitSha = %q, want %q", k, c.CommitSha, params.CommitSha)
				}
				// Facets must be a copy (not the same underlying map).
				if c.Facets["env"] != "dev" {
					t.Errorf("key %q: Facets[env] = %q, want dev", k, c.Facets["env"])
				}
			}

			// Output order should be deterministic (sorted by key).
			keys := make([]string, len(changes))
			for i, c := range changes {
				k := ""
				if c.Key != nil {
					k = *c.Key
				}
				keys[i] = k
			}
			if !sort.StringsAreSorted(keys) {
				t.Errorf("DiffKeyed() output not sorted by key: %v", keys)
			}
		})
	}
}
