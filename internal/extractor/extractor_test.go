package extractor_test

import (
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/extractor"
)

func TestExtract(t *testing.T) {
	t.Parallel()

	chartYAML := []byte(`
apiVersion: v2
name: aidp
version: 1.2.3
dependencies:
  - name: aidp-gateway
    version: "0.4.5"
    repository: oci://registry.example.com
`)

	tests := []struct {
		name        string
		content     []byte
		expr        string
		wantField   domain.TrackedField
		wantErrKind string // "none" | "compile" | "eval"
	}{
		{
			name:    "scalar top-level field present",
			content: chartYAML,
			expr:    ".version",
			wantField: domain.TrackedField{Value: "1.2.3", Present: true},
		},
		{
			name:    "scalar nested field present",
			content: chartYAML,
			expr:    ".dependencies[] | select(.name == \"aidp-gateway\") | .version",
			wantField: domain.TrackedField{Value: "0.4.5", Present: true},
		},
		{
			name:    "expression matches nothing returns absent",
			content: chartYAML,
			expr:    ".nonexistent",
			wantField: domain.TrackedField{Present: false},
		},
		{
			name:    "expression yields null returns absent",
			content: chartYAML,
			expr:    ".dependencies[] | select(.name == \"does-not-exist\") | .version",
			wantField: domain.TrackedField{Present: false},
		},
		{
			name:        "malformed jq expression returns compile error",
			content:     chartYAML,
			expr:        ".foo ||| bar",
			wantErrKind: "compile",
		},
		{
			name:    "nil content returns absent",
			content: nil,
			expr:    ".version",
			wantField: domain.TrackedField{Present: false},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ex, err := extractor.New(tc.expr)
			if tc.wantErrKind == "compile" {
				if err == nil {
					t.Fatal("expected compile error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q): unexpected error: %v", tc.expr, err)
			}

			got, err := ex.Extract(tc.content)
			if tc.wantErrKind == "eval" {
				if err == nil {
					t.Fatal("expected eval error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Extract: unexpected error: %v", err)
			}

			if got != tc.wantField {
				t.Errorf("Extract() = %+v, want %+v", got, tc.wantField)
			}
		})
	}
}
