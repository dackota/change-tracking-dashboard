package differ_test

import (
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/differ"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
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
			name:  "value unchanged produces no Change",
			field: "aidp-version",
			old:   domain.TrackedField{Value: "1.0.0", Present: true},
			new:   domain.TrackedField{Value: "1.0.0", Present: true},
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
