package extractor_test

import (
	"reflect"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/extractor"
)

// scalarFieldEqual compares two TrackedFields by their scalar fields only
// (Value and Present). Tests that only deal with scalar results use this so
// the Map field (which is a map and cannot use ==) doesn't cause a compile error.
func scalarFieldEqual(a, b domain.TrackedField) bool {
	return a.Value == b.Value && a.Present == b.Present
}

// chartWithDeps is a Chart.yaml with dependencies that have both alias and name
// keying, used for keyed extraction tests.
var chartWithDeps = []byte(`
apiVersion: v2
name: aidp
version: 2.0.0
dependencies:
  - name: kanpai-gateway
    alias: aidp-gateway
    version: "0.38.0"
    repository: oci://registry.example.com
  - name: kanpai-engine
    version: "1.2.0"
    repository: oci://registry.example.com
  - name: kanpai-ui
    alias: aidp-ui
    version: "0.5.0"
    repository: oci://registry.example.com
`)

// valuesYAML is a values.yaml with image tags, used for keyed extraction tests.
var valuesYAML = []byte(`
aidp-gateway:
  image:
    tag: "1.0.0"
aidp-engine:
  image:
    tag: "2.3.0"
aidp-ui:
  image:
    tag: "0.9.1"
other-key:
  notImage: "irrelevant"
`)

// TestExtractKeyed tests that Extract returns a keyed TrackedField when the gojq
// expression returns a JSON object (map). It covers the two real-world shapes
// from the PRD: subchart versions (alias-vs-name keying) and image tags.
func TestExtractKeyed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content []byte
		expr    string
		wantMap map[string]string // nil means expect absent (Present=false)
	}{
		{
			name:    "subchart versions with alias keying",
			content: chartWithDeps,
			// alias takes precedence over name when present
			expr: `.dependencies | map({(if .alias then .alias else .name end): .version}) | add`,
			wantMap: map[string]string{
				"aidp-gateway":  "0.38.0",
				"kanpai-engine": "1.2.0",
				"aidp-ui":       "0.5.0",
			},
		},
		{
			name:    "image tags from values.yaml",
			content: valuesYAML,
			expr:    `to_entries | map(select(.value.image.tag)) | map({(.key): .value.image.tag}) | add`,
			wantMap: map[string]string{
				"aidp-gateway": "1.0.0",
				"aidp-engine":  "2.3.0",
				"aidp-ui":      "0.9.1",
			},
		},
		{
			name:    "scalar path still returns scalar (not keyed)",
			content: chartWithDeps,
			expr:    `.version`,
			wantMap: nil, // scalar result — Map must be nil
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ex, err := extractor.New(tc.expr)
			if err != nil {
				t.Fatalf("New(%q): unexpected error: %v", tc.expr, err)
			}

			got, err := ex.Extract(tc.content)
			if err != nil {
				t.Fatalf("Extract: unexpected error: %v", err)
			}

			if tc.wantMap == nil {
				// Expect scalar result.
				if got.IsKeyed() {
					t.Errorf("Extract() returned keyed result, want scalar; Map = %v", got.Map)
				}
				if !got.Present {
					t.Errorf("Extract() Present=false, want true for scalar result")
				}
				return
			}

			// Expect keyed result.
			if !got.IsKeyed() {
				t.Fatalf("Extract() returned scalar result (Map=nil), want keyed map")
			}
			if !got.Present {
				t.Errorf("Extract() Present=false, want true for keyed result")
			}
			if !reflect.DeepEqual(got.Map, tc.wantMap) {
				t.Errorf("Extract() Map = %v, want %v", got.Map, tc.wantMap)
			}
		})
	}
}

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
			name:      "scalar top-level field present",
			content:   chartYAML,
			expr:      ".version",
			wantField: domain.TrackedField{Value: "1.2.3", Present: true},
		},
		{
			name:      "scalar nested field present",
			content:   chartYAML,
			expr:      ".dependencies[] | select(.name == \"aidp-gateway\") | .version",
			wantField: domain.TrackedField{Value: "0.4.5", Present: true},
		},
		{
			name:      "expression matches nothing returns absent",
			content:   chartYAML,
			expr:      ".nonexistent",
			wantField: domain.TrackedField{Present: false},
		},
		{
			name:      "expression yields null returns absent",
			content:   chartYAML,
			expr:      ".dependencies[] | select(.name == \"does-not-exist\") | .version",
			wantField: domain.TrackedField{Present: false},
		},
		{
			name:        "malformed jq expression returns compile error",
			content:     chartYAML,
			expr:        ".foo ||| bar",
			wantErrKind: "compile",
		},
		{
			name:      "nil content returns absent",
			content:   nil,
			expr:      ".version",
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

			if !scalarFieldEqual(got, tc.wantField) {
				t.Errorf("Extract() = %+v, want %+v", got, tc.wantField)
			}
		})
	}
}
